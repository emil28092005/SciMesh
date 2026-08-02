package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// TaskRunner spawns the Python task entry and returns the sealed partial
// artifact manifest it produced.
type TaskRunner struct {
	command []string
}

func NewTaskRunner(command []string) *TaskRunner {
	return &TaskRunner{command: command}
}

// Run executes one task: the task payload is written as JSON into the attempt
// directory, the Python entry computes and seals the partial, and the written
// manifest is parsed back. stderr is captured for failure reporting.
func (r *TaskRunner) Run(task *Task, taskDir string, manifestPath string, extraEnv []string) (*TaskRunnerManifest, error) {
	if err := os.MkdirAll(taskDir, 0o750); err != nil {
		return nil, err
	}
	payload := map[string]any{
		"task_id":          task.TaskID,
		"attempt":          task.Attempt,
		"lease_expires_at": task.leaseExpiresRaw,
		"workload":         task.Workload,
		"input": map[string]any{
			"uri":    task.Input.URI,
			"sha256": task.Input.SHA256,
		},
		"parameters": task.Parameters,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	taskJSONPath := filepath.Join(taskDir, "task.json")
	if err := os.WriteFile(taskJSONPath, payloadBytes, 0o600); err != nil {
		return nil, err
	}
	args := append([]string{}, r.command[1:]...)
	args = append(args,
		"--task-json", taskJSONPath,
		"--task-dir", taskDir,
		"--output", manifestPath,
	)
	// #nosec G204 -- the command comes from the operator-configured TASK_RUNNER.
	command := exec.CommandContext(context.Background(), r.command[0], args...)
	command.Dir = taskDir
	command.Env = append(os.Environ(), extraEnv...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, runnerExitError(exitErr.ExitCode(), stderr.String())
		}
		return nil, fmt.Errorf("task runner could not be started: %w", err)
	}
	// #nosec G304 -- the manifest path is inside the worker's own task directory.
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("task runner produced no result manifest")
	}
	var manifest TaskRunnerManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("task runner produced an invalid result manifest")
	}
	if manifest.ArtifactPath == "" || manifest.ContentType == "" {
		return nil, fmt.Errorf("task runner produced an incomplete result manifest")
	}
	info, err := os.Stat(manifest.ArtifactPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("task runner produced no artifact file")
	}
	return &manifest, nil
}
