package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ConfigFile is the persisted worker configuration written by the setup
// wizard and read back by `worker-agent --config`. Environment variables
// still win: the file fills in what the environment left unset.
type ConfigFile struct {
	CoordinatorURL string   `json:"coordinator_url"`
	Token          string   `json:"token,omitempty"`
	WorkerKey      string   `json:"worker_key,omitempty"`
	UserserviceURL string   `json:"userservice_url,omitempty"`
	WorkDir        string   `json:"work_dir"`
	WorkerName     string   `json:"worker_name,omitempty"`
	CPUCount       int      `json:"cpu_count"`
	MemoryMB       int      `json:"memory_mb"`
	TaskRunner     []string `json:"task_runner,omitempty"`
}

// DefaultConfigPath is where the setup wizard stores the worker's
// configuration. SCIMESH_WORKER_CONFIG overrides it.
func DefaultConfigPath() string {
	if raw := os.Getenv("SCIMESH_WORKER_CONFIG"); raw != "" {
		return raw
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".scimesh-worker", "config.json")
	}
	return filepath.Join(home, ".scimesh-worker", "config.json")
}

// LoadConfigFile reads and validates a persisted configuration. The file is
// created by the wizard with 0600 permissions, so no credential is exposed to
// other local users.
func LoadConfigFile(path string) (*Config, error) {
	//nolint:gosec // G304: path is --config or SCIMESH_WORKER_CONFIG, operator-supplied
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	var file ConfigFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}
	return file.Config()
}

// Config turns the file into the daemon configuration. Environment variables
// take precedence so operators can still override any value per-process.
func (f *ConfigFile) Config() (*Config, error) {
	config := &Config{}
	if env := os.Getenv("COORDINATOR_URL"); env != "" {
		config.CoordinatorURL = env
	} else {
		config.CoordinatorURL = strings.TrimSpace(f.CoordinatorURL)
	}
	if config.CoordinatorURL == "" {
		return nil, fmt.Errorf("coordinator_url is required")
	}
	if !strings.HasPrefix(config.CoordinatorURL, "http://") && !strings.HasPrefix(config.CoordinatorURL, "https://") {
		return nil, fmt.Errorf("coordinator_url must be an absolute HTTP(S) URL")
	}
	if env := os.Getenv("WORKER_AUTH_TOKEN"); env != "" {
		config.Token = env
	} else {
		config.Token = f.Token
	}
	if env := os.Getenv("WORKER_KEY"); env != "" {
		config.WorkerKey = env
	} else {
		config.WorkerKey = f.WorkerKey
	}
	if env := os.Getenv("USERSERVICE_URL"); env != "" {
		config.UserserviceURL = env
	} else {
		config.UserserviceURL = f.UserserviceURL
	}
	if env := os.Getenv("WORK_DIR"); env != "" {
		config.WorkDir = env
	} else if f.WorkDir != "" {
		config.WorkDir = f.WorkDir
	} else {
		config.WorkDir = "./scimesh-agent-data"
	}
	if env := os.Getenv("WORKER_NAME"); env != "" {
		config.WorkerName = env
	} else {
		config.WorkerName = f.WorkerName
	}
	config.CPUCount = f.CPUCount
	if config.CPUCount < 1 {
		config.CPUCount = 1
	}
	config.MemoryMB = f.MemoryMB
	if config.MemoryMB < 0 {
		config.MemoryMB = 0
	}
	if len(f.TaskRunner) > 0 {
		config.TaskRunner = f.TaskRunner
	}
	if len(config.TaskRunner) == 0 {
		config.TaskRunner = []string{"python", "-m", "scimesh.worker.task"}
	}
	config.PollInterval = 2 * time.Second
	config.RequestTimeout = 30 * time.Second
	config.Heartbeat = 15 * time.Second
	config.Capabilities = DefaultCapabilities()
	return config, nil
}

// Save writes the configuration file, creating the parent directory and
// restricting permissions to the owner.
func SaveConfigFile(path string, file ConfigFile) error {
	payload, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	return nil
}
