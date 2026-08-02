// Package agent implements a Go worker agent: a coordinator client and
// task-lifecycle supervisor that executes SDK workloads in a Python
// subprocess. It mirrors the Python worker's v1 wire contract exactly; the
// Python worker remains the reference implementation.
package agent

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	uuidPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	workloadPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[_-][a-z0-9]+)*$`)
)

// RegisteredWorker is the coordinator's answer to /workers/register.
type RegisteredWorker struct {
	WorkerID                 string
	HeartbeatIntervalSeconds float64
}

// Input is the claimed task's input artifact.
type Input struct {
	URI    string
	SHA256 string
}

// Task is one claimed, leased task.
type Task struct {
	TaskID          string
	Attempt         int
	LeaseExpiresAt  time.Time
	Workload        string
	Input           Input
	Parameters      map[string]any
	leaseExpiresRaw string
}

// Uploaded is the coordinator-owned metadata returned after artifact upload.
type Uploaded struct {
	ArtifactID string
	URI        string
	SHA256     string
	SizeBytes  int64
}

func requireString(value any, field string) (string, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s must be a non-empty string", field)
	}
	return text, nil
}

func safeCoordinatorURI(value any, field string) (string, error) {
	uri, err := requireString(value, field)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(uri, "/") {
		// A network-path reference (//host/path) or dot segments would
		// resolve to another origin; reject both.
		if strings.HasPrefix(uri, "//") {
			return "", fmt.Errorf("%s must be a safe coordinator path", field)
		}
		for _, segment := range strings.Split(uri, "/") {
			if segment == ".." {
				return "", fmt.Errorf("%s must be a safe coordinator path", field)
			}
		}
		return uri, nil
	}
	parsed, err := url.Parse(uri)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("%s must be an absolute HTTP(S) URL or coordinator path", field)
	}
	return uri, nil
}

func sha256Hex(value any, field string) (string, error) {
	digest, err := requireString(value, field)
	if err != nil {
		return "", err
	}
	digest = strings.ToLower(digest)
	if !sha256Pattern.MatchString(digest) {
		return "", fmt.Errorf("%s must be a SHA-256 hex digest", field)
	}
	return digest, nil
}

func uuid(value any, field string) (string, error) {
	text, err := requireString(value, field)
	if err != nil {
		return "", err
	}
	if !uuidPattern.MatchString(text) {
		return "", fmt.Errorf("%s must be a UUID", field)
	}
	return strings.ToLower(text), nil
}

// ParseTask validates a claimed-task response with the same strictness as the
// Python worker's ClaimedTask.from_json.
func ParseTask(payload map[string]any) (*Task, error) {
	rawInput, ok := payload["input"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("input must be an object")
	}
	rawAttempt, ok := payload["attempt"].(float64)
	if !ok || rawAttempt < 1 || rawAttempt != float64(int(rawAttempt)) {
		return nil, fmt.Errorf("attempt must be a positive integer")
	}
	taskID, err := uuid(payload["task_id"], "task_id")
	if err != nil {
		return nil, fmt.Errorf("invalid claimed-task response: %w", err)
	}
	rawLease, err := requireString(payload["lease_expires_at"], "lease_expires_at")
	if err != nil {
		return nil, fmt.Errorf("invalid claimed-task response: %w", err)
	}
	lease, err := time.Parse(time.RFC3339, rawLease)
	if err != nil || lease.Location() == nil {
		return nil, fmt.Errorf("lease_expires_at must include a timezone")
	}
	workload, err := requireString(payload["workload"], "workload")
	if err != nil || !workloadPattern.MatchString(workload) {
		return nil, fmt.Errorf("workload must be a canonical name")
	}
	uri, err := safeCoordinatorURI(rawInput["uri"], "input.uri")
	if err != nil {
		return nil, fmt.Errorf("invalid claimed-task response: %w", err)
	}
	digest, err := sha256Hex(rawInput["sha256"], "input.sha256")
	if err != nil {
		return nil, fmt.Errorf("invalid claimed-task response: %w", err)
	}
	parameters, ok := payload["parameters"].(map[string]any)
	if !ok {
		parameters = map[string]any{}
	}
	return &Task{
		TaskID:          taskID,
		Attempt:         int(rawAttempt),
		LeaseExpiresAt:  lease,
		leaseExpiresRaw: rawLease,
		Workload:        workload,
		Input:           Input{URI: uri, SHA256: digest},
		Parameters:      parameters,
	}, nil
}

// LeaseExpiresRaw returns the original lease timestamp string for
// round-tripping in heartbeat deadlines.
func (t *Task) LeaseExpiresRaw() string { return t.leaseExpiresRaw }

// ParseRegistered validates a registration response.
func ParseRegistered(payload map[string]any) (*RegisteredWorker, error) {
	workerID, err := uuid(payload["worker_id"], "worker_id")
	if err != nil {
		return nil, fmt.Errorf("invalid worker registration response: %w", err)
	}
	interval, ok := payload["heartbeat_interval_seconds"].(float64)
	if !ok || interval <= 0 {
		return nil, fmt.Errorf("heartbeat_interval_seconds must be positive")
	}
	return &RegisteredWorker{WorkerID: workerID, HeartbeatIntervalSeconds: interval}, nil
}

// ParseUploaded validates an artifact upload response.
func ParseUploaded(payload map[string]any) (*Uploaded, error) {
	artifactID, err := uuid(payload["artifact_id"], "artifact_id")
	if err != nil {
		return nil, fmt.Errorf("invalid artifact upload response: %w", err)
	}
	uri, err := safeCoordinatorURI(payload["uri"], "uri")
	if err != nil {
		return nil, fmt.Errorf("invalid artifact upload response: %w", err)
	}
	digest, err := sha256Hex(payload["sha256"], "sha256")
	if err != nil {
		return nil, fmt.Errorf("invalid artifact upload response: %w", err)
	}
	rawSize, ok := payload["size_bytes"].(float64)
	if !ok || rawSize < 0 || rawSize != float64(int64(rawSize)) {
		return nil, fmt.Errorf("artifact size_bytes must be a non-negative integer")
	}
	return &Uploaded{ArtifactID: artifactID, URI: uri, SHA256: digest, SizeBytes: int64(rawSize)}, nil
}

// TaskRunnerManifest is what the Python task entry writes on success.
type TaskRunnerManifest struct {
	ArtifactPath string         `json:"artifact_path"`
	ContentType  string         `json:"content_type"`
	Metrics      map[string]any `json:"metrics"`
}

// Encode serializes a claim payload for /tasks/claim.
func Encode(v any) ([]byte, error) { return json.Marshal(v) }
