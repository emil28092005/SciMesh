// Package setupui serves the local worker setup wizard: a small HTTP server
// bound to 127.0.0.1 that writes the worker's config file, runs preflight
// checks, and starts/stops the worker as a background process. It is part of
// the worker-agent binary so a machine that installs only a worker never needs
// a coordinator.
package setupui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/agent"
)

//go:embed template.html
var templateFS embed.FS

const (
	defaultPort = 12700
	pidFileName = "worker.pid"
	logFileName = "worker.log"
)

// Supervisor starts and stops the worker process and tracks its pid. It is an
// interface so tests can substitute a fake.
type Supervisor interface {
	// Start launches `worker-agent --config <path>` detached, writing output
	// into the log file. Returns the child pid.
	Start(configPath, logPath string) (int, error)
	// Stop terminates the process recorded in the pid file.
	Stop() error
	// Pid returns the recorded child pid, or 0 when none is recorded.
	Pid() int
	// Alive reports whether the recorded child is still running.
	Alive() bool
}

// PIDSupervisor is the real Supervisor: it spawns the running binary with
// --config and manages its pid file. Liveness comes from a Wait goroutine, so
// it works on every platform (no signal probing, which Windows lacks).
type PIDSupervisor struct {
	mu      sync.Mutex
	pidPath string
	proc    *os.Process
	done    chan struct{} // closed when the spawned process exits; nil when not started
}

func NewPIDSupervisor(pidPath string) *PIDSupervisor { return &PIDSupervisor{pidPath: pidPath} }

func (s *PIDSupervisor) Pid() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readPid()
}

func (s *PIDSupervisor) readPid() int {
	raw, err := os.ReadFile(s.pidPath)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid < 1 {
		return 0
	}
	return pid
}

func (s *PIDSupervisor) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *PIDSupervisor) Start(configPath, logPath string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		select {
		case <-s.done:
		default:
			return s.readPid(), fmt.Errorf("worker is already running (pid %d)", s.readPid())
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("resolve worker binary: %w", err)
	}
	//nolint:gosec // G304: logPath lives in the wizard's own config directory
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open worker log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	//nolint:gosec // G204: exe is os.Executable, configPath is the wizard's own file;
	// Background ctx: the child's lifecycle is managed by the supervisor, not the context
	cmd := exec.CommandContext(context.Background(), exe, "--config", configPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start worker: %w", err)
	}
	// The child inherits our stdout/stderr descriptors pointing at the log
	// file, so we can close our copy; the child keeps it open.
	_ = logFile.Close()
	s.proc = cmd.Process
	s.done = make(chan struct{})
	go func() { _ = cmd.Wait(); close(s.done) }()
	if err := os.WriteFile(s.pidPath, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600); err != nil {
		_ = cmd.Process.Kill()
		return 0, fmt.Errorf("write pid file: %w", err)
	}
	return cmd.Process.Pid, nil
}

func (s *PIDSupervisor) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		return nil
	}
	select {
	case <-s.done:
		s.done = nil
		s.proc = nil
		_ = os.Remove(s.pidPath)
		return nil
	default:
	}
	// Ask politely, then force. os.Interrupt terminates on Windows too.
	_ = s.proc.Signal(os.Interrupt)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-s.done:
			s.done = nil
			s.proc = nil
			_ = os.Remove(s.pidPath)
			return nil
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
	_ = s.proc.Kill()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
	s.done = nil
	s.proc = nil
	_ = os.Remove(s.pidPath)
	return nil
}

// Server is the wizard HTTP server, bound to the loopback interface only.
type Server struct {
	log           *slog.Logger
	cfgPath       string
	logPath       string
	dir           string
	sup           Supervisor
	openBrowser   func(string)
	port          int
	install       func(ctx context.Context, venvPython, pkg string) error
	downloadWheel func(ctx context.Context, url, dir string) (string, error)
}

// Options customises the wizard for tests and embedding.
type Options struct {
	Port        int
	ConfigPath  string
	OpenBrowser func(url string)
	Supervisor  Supervisor
	Dir         string // directory for pid/log files; defaults to the config dir
	// InstallScimesh overrides the pip step of the runtime installer (tests
	// substitute a fake); nil uses the real pip inside the managed venv.
	InstallScimesh func(ctx context.Context, venvPython, pkg string) error
	// DownloadWheel overrides the release-wheel download (tests substitute a
	// fake); nil downloads from the GitHub release matching the agent version.
	DownloadWheel func(ctx context.Context, url, dir string) (string, error)
}

func New(log *slog.Logger, opts Options) *Server {
	cfgPath := opts.ConfigPath
	if cfgPath == "" {
		cfgPath = agent.DefaultConfigPath()
	}
	dir := opts.Dir
	if dir == "" {
		dir = filepath.Dir(cfgPath)
	}
	sup := opts.Supervisor
	if sup == nil {
		sup = NewPIDSupervisor(filepath.Join(dir, pidFileName))
	}
	open := opts.OpenBrowser
	if open == nil {
		open = func(string) {}
	}
	port := opts.Port
	if port == 0 {
		port = defaultPort
	}
	install := opts.InstallScimesh
	if install == nil {
		install = installScimeshWithPip
	}
	downloadWheel := opts.DownloadWheel
	if downloadWheel == nil {
		downloadWheel = agent.DownloadWheel
	}
	return &Server{log: log, cfgPath: cfgPath, logPath: filepath.Join(dir, logFileName), dir: dir, sup: sup, openBrowser: open, port: port, install: install, downloadWheel: downloadWheel}
}

// Listen binds the loopback listener and returns it; Serve runs the server on
// it. Split so tests can inspect the actual ephemeral port.
func (s *Server) Listen() (net.Listener, error) {
	return (&net.ListenConfig{}).Listen(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
}

// OpenBrowser hands the wizard URL to the configured opener (default: no-op).
func (s *Server) OpenBrowser(url string) { s.openBrowser(url) }

// Serve runs the wizard until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/config", s.handleSaveConfig)
	mux.HandleFunc("POST /api/test", s.handleTest)
	mux.HandleFunc("POST /api/runtime/install", s.handleInstallRuntime)
	mux.HandleFunc("POST /api/start", s.handleStart)
	mux.HandleFunc("POST /api/stop", s.handleStop)
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	return server.Serve(listener)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	html, err := templateFS.ReadFile("template.html")
	if err != nil {
		http.Error(w, "template unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(html)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// statusView is what the wizard needs to paint the running/stopped state.
type statusView struct {
	ConfigPresent bool        `json:"config_present"`
	ConfigPath    string      `json:"config_path"`
	LogPath       string      `json:"log_path"`
	Running       bool        `json:"running"`
	Pid           int         `json:"pid"`
	WorkerName    string      `json:"worker_name,omitempty"`
	Coordinator   string      `json:"coordinator,omitempty"`
	WorkDir       string      `json:"work_dir,omitempty"`
	TokenSet      bool        `json:"token_set"`
	Stats         WorkerStats `json:"stats"`
}

// WorkerStats is parsed from the worker log: the agent reports each claim,
// completion and failure as a structured line, so the wizard can show live
// counters without any coordinator access.
type WorkerStats struct {
	Registered bool `json:"registered"`
	Claimed    int  `json:"claimed"`
	Completed  int  `json:"completed"`
	Failed     int  `json:"failed"`
}

// ensureVenvTaskRunner rewrites the saved config so its task runner uses the
// managed venv python when one exists and the config does not already pin one.
func (s *Server) ensureVenvTaskRunner() {
	raw, err := os.ReadFile(s.cfgPath)
	if err != nil {
		return
	}
	var file agent.ConfigFile
	if json.Unmarshal(raw, &file) != nil || len(file.TaskRunner) > 0 {
		return
	}
	if venv := s.venvPython(); venv != "" {
		file.TaskRunner = []string{venv, "-I", "-m", "scimesh.worker.task"}
		if payload, err := json.MarshalIndent(file, "", "  "); err == nil {
			_ = os.WriteFile(s.cfgPath, append(payload, '\n'), 0o600)
		}
	}
}

// parseWorkerStats counts the structured agent events in the worker log.
func parseWorkerStats(logPath string) WorkerStats {
	var stats WorkerStats
	//nolint:gosec // G304: logPath is the wizard's own log file next to the config
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return stats
	}
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.Contains(line, "msg=registered"):
			stats.Registered = true
		case strings.Contains(line, `msg="task claimed"`):
			stats.Claimed++
		case strings.Contains(line, `msg="task completed"`):
			stats.Completed++
		case strings.Contains(line, `msg="task failed"`):
			stats.Failed++
		}
	}
	return stats
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	view := statusView{ConfigPath: s.cfgPath, LogPath: s.logPath, Running: s.sup.Alive(), Pid: s.sup.Pid(), Stats: parseWorkerStats(s.logPath)}
	if raw, err := os.ReadFile(s.cfgPath); err == nil {
		var file agent.ConfigFile
		if json.Unmarshal(raw, &file) == nil {
			view.ConfigPresent = true
			view.WorkerName = file.WorkerName
			view.Coordinator = file.CoordinatorURL
			view.WorkDir = file.WorkDir
			view.TokenSet = file.Token != "" || file.WorkerKey != ""
		}
	}
	writeJSON(w, http.StatusOK, view)
}

type saveConfigRequest struct {
	CoordinatorURL string   `json:"coordinator_url"`
	Token          string   `json:"token"`
	WorkerKey      string   `json:"worker_key"`
	UserserviceURL string   `json:"userservice_url"`
	WorkDir        string   `json:"work_dir"`
	WorkerName     string   `json:"worker_name"`
	CPUCount       int      `json:"cpu_count"`
	MemoryMB       int      `json:"memory_mb"`
	Concurrency    int      `json:"concurrency"`
	TaskRunner     []string `json:"task_runner"`
}

func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	var req saveConfigRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	file := agent.ConfigFile{
		CoordinatorURL: strings.TrimSpace(req.CoordinatorURL),
		Token:          req.Token,
		WorkerKey:      req.WorkerKey,
		UserserviceURL: strings.TrimSpace(req.UserserviceURL),
		WorkDir:        strings.TrimSpace(req.WorkDir),
		WorkerName:     strings.TrimSpace(req.WorkerName),
		CPUCount:       req.CPUCount,
		MemoryMB:       req.MemoryMB,
		Concurrency:    req.Concurrency,
		TaskRunner:     req.TaskRunner,
	}
	if file.CoordinatorURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "coordinator_url is required"})
		return
	}
	if !strings.HasPrefix(file.CoordinatorURL, "http://") && !strings.HasPrefix(file.CoordinatorURL, "https://") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "coordinator_url must be an absolute HTTP(S) URL"})
		return
	}
	if file.WorkDir == "" {
		file.WorkDir = "./scimesh-agent-data"
	}
	if file.WorkerName == "" {
		if host, err := os.Hostname(); err == nil {
			file.WorkerName = host
		} else {
			file.WorkerName = "worker"
		}
	}
	if file.CPUCount < 1 {
		file.CPUCount = 1
	}
	if file.Concurrency < 1 {
		file.Concurrency = 1
	}
	// The wizard UI bakes the venv python into the runner after an install;
	// an API-driven or scripted flow may not, so the server guarantees it:
	// workloads execute through scimesh's task runner, which lives in the venv.
	if len(file.TaskRunner) == 0 {
		if venv := s.venvPython(); venv != "" {
			file.TaskRunner = []string{venv, "-I", "-m", "scimesh.worker.task"}
		}
	}
	if err := agent.SaveConfigFile(s.cfgPath, file); err != nil {
		s.log.Error("save wizard config", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not write the config file"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	var req saveConfigRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	url := strings.TrimSpace(req.CoordinatorURL)
	if url == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "coordinator_url is required"})
		return
	}
	// After the runtime installer created the venv, probe that interpreter:
	// checking the bare system python3 would keep reporting scimesh as
	// missing even though the worker would run with the venv.
	report := agent.RunCheck(r.Context(), url, s.venvPython(), req.Token, req.WorkerKey, req.UserserviceURL)
	if report.Scimesh.OK {
		report.Scimesh = ensureMatchingScimeshVersion(report.Scimesh)
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if _, err := os.Stat(s.cfgPath); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no configuration saved yet"})
		return
	}
	s.ensureVenvTaskRunner()
	pid, err := s.sup.Start(s.cfgPath, s.logPath)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"pid": pid})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if err := s.sup.Stop(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": true})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	raw, err := os.ReadFile(s.logPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"log": ""})
		return
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	tail := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil && n > 0 && n < 5000 {
		tail = n
	}
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	writeJSON(w, http.StatusOK, map[string]string{"log": strings.Join(lines, "\n")})
}

// ErrCanceled mirrors context.Canceled for callers that treat a cancelled
// wizard as a clean exit.
var ErrCanceled = errors.New("setup wizard cancelled")

type installRuntimeRequest struct {
	// ScimeshPackage overrides where the scimesh wheel comes from: a local
	// wheel/index path or the PyPI name. Defaults to SCIMESH_PIP_PACKAGE, then
	// to the PyPI name.
	ScimeshPackage string `json:"scimesh_package"`
}

type installRuntimeResponse struct {
	OK        bool   `json:"ok"`
	Python    string `json:"python,omitempty"` // venv python to use as TASK_RUNNER[0]
	Installed bool   `json:"installed"`
}

// handleInstallRuntime creates a managed venv next to the worker config and
// installs the scimesh package into it, so the machine needs no manual pip
// step. The venv python path is returned for the wizard to bake into the
// task runner.
func (s *Server) handleInstallRuntime(w http.ResponseWriter, r *http.Request) {
	// The wizard may run the install before any config was saved, so the
	// worker directory (venv, wheel) may not exist yet.
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "could not create the worker directory"})
		return
	}
	var req installRuntimeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	pkg := strings.TrimSpace(req.ScimeshPackage)
	if pkg == "" {
		pkg = os.Getenv("SCIMESH_PIP_PACKAGE")
	}
	if pkg == "" {
		// No PyPI default on purpose: the PyPI name "scimesh" belongs to an
		// unrelated project. Instead we ship the wheel in our own GitHub
		// release, version-locked to this binary, and download it from there.
		url, _, err := agent.ReleaseWheelURL(agent.Version)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "no scimesh source configured: set SCIMESH_PIP_PACKAGE to your wheel, checkout or index, then retry",
			})
			return
		}
		local, err := s.downloadWheel(r.Context(), url, s.dir)
		if err != nil {
			s.log.Error("download release wheel", "err", err, "url", url)
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "could not download the scimesh wheel for this release: " + err.Error() + ". Set SCIMESH_PIP_PACKAGE to your wheel, checkout or index and retry.",
			})
			return
		}
		pkg = local
	}

	python3, err := exec.LookPath("python3")
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "python3 is not installed on this machine"})
		return
	}
	venvDir := filepath.Join(s.dir, "venv")
	venvPython := filepath.Join(venvDir, "bin", "python")
	if _, err := os.Stat(venvPython); err != nil {
		// Windows layout: Scripts/python.exe.
		if win := filepath.Join(venvDir, "Scripts", "python.exe"); stat(win) {
			venvPython = win
		}
	}
	if _, err := os.Stat(venvPython); err != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, python3, "-m", "venv", venvDir) //nolint:gosec // G204: python3 from LookPath, venvDir is our own dir
		if out, err := cmd.CombinedOutput(); err != nil {
			s.log.Error("create runtime venv", "err", err, "out", truncate(string(out), 500))
			writeJSON(w, http.StatusConflict, map[string]string{"error": "could not create the python venv"})
			return
		}
	}

	if err := s.install(r.Context(), venvPython, pkg); err != nil {
		s.log.Error("install scimesh runtime", "err", err)
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "pip install " + pkg + " failed: " + err.Error() +
				". Set SCIMESH_PIP_PACKAGE to your scimesh wheel or index and retry.",
		})
		return
	}
	writeJSON(w, http.StatusOK, installRuntimeResponse{OK: true, Python: venvPython, Installed: true})
}

// venvPython returns the managed venv python when the runtime installer has
// created one, so the task runner can be pointed at it automatically.
func (s *Server) venvPython() string {
	for _, candidate := range []string{
		filepath.Join(s.dir, "venv", "bin", "python"),
		filepath.Join(s.dir, "venv", "Scripts", "python.exe"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// installScimeshWithPip installs the package with the venv's own pip,
// streaming into the agent log so a long build is not silent.
func installScimeshWithPip(ctx context.Context, venvPython, pkg string) error {
	pip := filepath.Join(filepath.Dir(venvPython), "pip")
	if _, err := os.Stat(pip); err != nil {
		pip += ".exe"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, pip, "install", pkg) //nolint:gosec // G204: pip from our venv, pkg is operator-set or a fixed default
	return cmd.Run()
}

func stat(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ensureMatchingScimeshVersion flips a green scimesh check to a stale one when
// the installed package does not match the worker-agent's own version: a
// version-locked wheel is the only supported runtime, and a mismatch means the
// workload catalog the worker advertises is not what it executes. The wizard
// UI then offers the Install button again. Dev builds have no release wheel,
// so they skip the comparison.
func ensureMatchingScimeshVersion(item agent.CheckItem) agent.CheckItem {
	if agent.Version == "" || agent.Version == "dev" {
		return item
	}
	want := agent.NormalizePEP440(agent.Version)
	got := strings.TrimSpace(item.Detail)
	if got == "" || got == want {
		return item
	}
	item.OK = false
	item.Detail = fmt.Sprintf(
		"installed scimesh %s, but this worker-agent (%s) needs %s — press Install to upgrade",
		got, agent.Version, want,
	)
	return item
}
