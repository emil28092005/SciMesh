package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Outcome reports whether a claim was made and whether it completed.
type Outcome struct {
	Claimed   bool
	Completed bool
}

// Daemon is the agent state machine: register, claim, execute via the Python
// task runner, upload, and submit — mirroring the Python worker's lifecycle.
type Daemon struct {
	config     *Config
	client     *Client
	runner     *TaskRunner
	log        *slog.Logger
	workerID   string
	registered bool
	completed  int
	mu         sync.Mutex
}

func NewDaemon(config *Config, client *Client, runner *TaskRunner, log *slog.Logger) *Daemon {
	return &Daemon{config: config, client: client, runner: runner, log: log}
}

// RunForever loops until interrupted, idle-exit, or max tasks.
func (d *Daemon) RunForever() error {
	failures := 0
	for {
		if !d.registered {
			if err := d.register(); err != nil {
				return err
			}
		}
		outcome, err := d.runOnce()
		if err != nil {
			failures++
			d.log.Warn("agent cycle failed", "error", err)
			backoff := d.config.PollInterval
			for i := 0; i < failures && i < 6; i++ {
				backoff *= 2
			}
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
			time.Sleep(backoff)
			continue
		}
		failures = 0
		if outcome.Claimed && outcome.Completed {
			d.completed++
			if d.config.MaxTasks > 0 && d.completed >= d.config.MaxTasks {
				d.log.Info("max tasks reached")
				return nil
			}
		}
		if !outcome.Claimed && d.config.ExitWhenIdle {
			d.log.Info("queue empty, exiting")
			return nil
		}
		if outcome.Claimed && d.config.ExitWhenIdle {
			d.log.Info("one claim processed, exiting")
			return nil
		}
		if !outcome.Claimed {
			time.Sleep(d.config.PollInterval)
		}
	}
}

func (d *Daemon) register() error {
	registered, err := d.client.Register(
		d.config.WorkerName,
		d.config.Capabilities,
		d.config.CPUCount,
		d.config.MemoryMB,
	)
	if err != nil {
		return err
	}
	d.mu.Lock()
	if d.config.WorkerID != "" {
		d.workerID = d.config.WorkerID
	} else {
		d.workerID = registered.WorkerID
	}
	d.registered = true
	d.mu.Unlock()
	d.log.Info("registered", "worker_id", d.workerID)
	return nil
}

func (d *Daemon) workerIDOrEmpty() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.workerID
}

func (d *Daemon) runOnce() (Outcome, error) {
	workerID := d.workerIDOrEmpty()
	if workerID == "" {
		return Outcome{}, fmt.Errorf("agent is not registered")
	}
	task, err := d.client.Claim(workerID, d.config.Capabilities)
	if err != nil {
		return Outcome{}, err
	}
	if task == nil {
		return Outcome{Claimed: false}, nil
	}
	started := time.Now()
	taskDir := filepath.Join(d.config.WorkDir, task.TaskID, fmt.Sprint(task.Attempt))
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return Outcome{Claimed: true}, err
	}
	heartbeat := newLeaseHeartbeat(task, workerID, d.client, d.config.Heartbeat)
	completed := false
	err = heartbeat.Start()
	if err != nil {
		if _, ok := err.(*ConflictError); ok {
			d.log.Warn("lease lost", "task_id", task.TaskID)
			return Outcome{Claimed: true}, nil
		}
		return Outcome{Claimed: true}, err
	}
	defer heartbeat.Stop()

	// Attempt directory cleanup is deliberately minimal in the prototype:
	// attempt directories are retained under the work directory.
	failure := d.executeTask(task, workerID, taskDir, started, heartbeat)
	if failure != nil {
		if _, ok := failure.(*ConflictError); ok {
			d.log.Warn("lease lost", "task_id", task.TaskID)
			return Outcome{Claimed: true}, nil
		}
		if err := heartbeat.RaiseIfFailed(); err != nil {
			return Outcome{Claimed: true}, nil
		}
		d.reportFailure(task, workerID, failure)
		return Outcome{Claimed: true}, nil
	}
	if err := heartbeat.RaiseIfFailed(); err != nil {
		return Outcome{Claimed: true}, nil
	}
	completed = true
	d.log.Info("task completed", "task_id", task.TaskID, "elapsed_seconds", time.Since(started).Seconds())
	return Outcome{Claimed: true, Completed: completed}, nil
}

// executeTask returns nil on success or a classified failure.
func (d *Daemon) executeTask(task *Task, workerID, taskDir string, started time.Time, heartbeat *leaseHeartbeat) error {
	inputPath := filepath.Join(taskDir, "input")
	// Downloads use the coordinator-provided URI verbatim; a relative path is
	// resolved against the coordinator by the client.
	actualSHA, err := d.client.Download(task.Input.URI, inputPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actualSHA, task.Input.SHA256) {
		return &CoordinatorError{msg: "input checksum mismatch"}
	}
	if err := heartbeat.RaiseIfFailed(); err != nil {
		return err
	}
	manifestPath := filepath.Join(taskDir, "manifest.json")
	manifest, err := d.runner.Run(task, taskDir, manifestPath, nil)
	if err != nil {
		return err
	}
	if err := heartbeat.RaiseIfFailed(); err != nil {
		return err
	}
	uploaded, err := d.client.Upload(task, workerID, manifest.ArtifactPath, manifest.ContentType)
	if err != nil {
		return err
	}
	if err := heartbeat.RaiseIfFailed(); err != nil {
		return err
	}
	metrics := map[string]any{"elapsed_seconds": roundSeconds(time.Since(started).Seconds())}
	for name, value := range manifest.Metrics {
		metrics[name] = value
	}
	return d.client.Submit(task, workerID, uploaded, metrics)
}

func (d *Daemon) reportFailure(task *Task, workerID string, failure error) {
	var code string
	switch failure.(type) {
	case *CoordinatorError:
		code = "ValueError"
	default:
		code = "TaskRunnerFailed"
	}
	retryable := IsRetryableError(failure)
	message := SanitizeErrorMessage(failure.Error(), d.config.WorkDir)
	d.log.Warn("task failed", "task_id", task.TaskID, "error_code", code, "retryable", retryable)
	if err := d.client.Fail(task, workerID, code, message, retryable); err != nil {
		if _, ok := err.(*ConflictError); ok {
			d.log.Warn("lease lost while reporting failure", "task_id", task.TaskID)
			return
		}
		d.log.Warn("failure report rejected", "task_id", task.TaskID, "error", err)
	}
}

// leaseHeartbeat renews the lease from the returned deadline at less than
// half of the remaining TTL, mirroring the Python worker.
type leaseHeartbeat struct {
	task     *Task
	workerID string
	client   *Client
	interval time.Duration
	stop     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	lease    time.Time
	failed   error
}

func newLeaseHeartbeat(task *Task, workerID string, client *Client, interval time.Duration) *leaseHeartbeat {
	return &leaseHeartbeat{
		task:     task,
		workerID: workerID,
		client:   client,
		interval: interval,
		stop:     make(chan struct{}),
		lease:    task.LeaseExpiresAt,
	}
}

func (h *leaseHeartbeat) Start() error {
	if _, err := h.client.Heartbeat(h.task, h.workerID); err != nil {
		return err
	}
	go h.loop()
	return nil
}

func (h *leaseHeartbeat) Stop() {
	h.once.Do(func() { close(h.stop) })
}

func (h *leaseHeartbeat) RaiseIfFailed() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.failed
}

func (h *leaseHeartbeat) loop() {
	for {
		delay := h.nextDelay()
		select {
		case <-h.stop:
			return
		case <-time.After(delay):
		}
		renewed, err := h.client.Heartbeat(h.task, h.workerID)
		h.mu.Lock()
		if err != nil {
			h.failed = err
			h.mu.Unlock()
			return
		}
		h.lease = renewed
		h.mu.Unlock()
	}
}

func (h *leaseHeartbeat) nextDelay() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	remaining := time.Until(h.lease)
	if remaining <= 0 {
		return 0
	}
	half := remaining / 2
	if h.interval < half {
		return h.interval
	}
	return half
}

func roundSeconds(seconds float64) float64 {
	return float64(int64(seconds*1000)) / 1000
}

// File-digest helper used by tests.
func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
