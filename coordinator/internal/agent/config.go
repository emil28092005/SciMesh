package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is read only from the environment, mirroring the former Python
// worker's configuration surface.
type Config struct {
	CoordinatorURL string
	Token          string
	WorkerKey      string
	UserserviceURL string
	WorkerName     string
	WorkerID       string // set after registration; overridable for tests
	WorkDir        string
	CPUCount       int
	MemoryMB       int // 0 = not advertised
	PollInterval   time.Duration
	RequestTimeout time.Duration
	Heartbeat      time.Duration
	CleanupAfter   time.Duration // 0 = keep attempt directories
	Capabilities   []string
	TaskRunner     []string // command + args; defaults to python -m scimesh.worker.task
	MaxTasks       int      // 0 = unlimited
	ExitWhenIdle   bool
}

func envList(name string) ([]string, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return nil, nil
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array", name)
	}
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			return nil, fmt.Errorf("%s must not contain empty entries", name)
		}
	}
	return items, nil
}

// LoadConfig validates the environment and fails fast on invalid values.
func LoadConfig() (*Config, error) {
	url := os.Getenv("COORDINATOR_URL")
	if url == "" {
		return nil, fmt.Errorf("COORDINATOR_URL is required")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("COORDINATOR_URL must be an absolute HTTP(S) URL")
	}
	workDir := os.Getenv("WORK_DIR")
	if workDir == "" {
		workDir = "./scimesh-agent-data"
	}
	cpu := 1
	if raw := os.Getenv("CPU_COUNT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return nil, fmt.Errorf("CPU_COUNT must be a positive integer")
		}
		cpu = parsed
	}
	memoryMB := 0
	if raw := os.Getenv("MEMORY_MB"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return nil, fmt.Errorf("MEMORY_MB must be a positive integer")
		}
		memoryMB = parsed
	}
	poll, err := durationEnv("POLL_INTERVAL", 2*time.Second)
	if err != nil {
		return nil, err
	}
	timeout, err := durationEnv("REQUEST_TIMEOUT", 30*time.Second)
	if err != nil {
		return nil, err
	}
	heartbeat, err := durationEnv("HEARTBEAT_INTERVAL", 15*time.Second)
	if err != nil {
		return nil, err
	}
	cleanup, err := durationEnv("CLEANUP_AFTER_SECONDS", 0)
	if err != nil {
		return nil, err
	}
	capabilities, err := envList("CAPABILITIES")
	if err != nil {
		return nil, err
	}
	if len(capabilities) == 0 {
		capabilities = []string{"similarity-search", "similarity_search"}
	}
	runner, err := envList("TASK_RUNNER")
	if err != nil {
		return nil, err
	}
	if len(runner) == 0 {
		runner = []string{"python", "-m", "scimesh.worker.task"}
	}
	maxTasks := 0
	if raw := os.Getenv("MAX_TASKS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return nil, fmt.Errorf("MAX_TASKS must be a positive integer")
		}
		maxTasks = parsed
	}
	name := os.Getenv("WORKER_NAME")
	if name == "" {
		host, _ := os.Hostname()
		name = host
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("WORK_DIR must be an absolute path")
	}
	return &Config{
		CoordinatorURL: strings.TrimRight(url, "/"),
		Token:          os.Getenv("WORKER_AUTH_TOKEN"),
		WorkerKey:      os.Getenv("WORKER_KEY"),
		UserserviceURL: strings.TrimRight(os.Getenv("USERSERVICE_URL"), "/"),
		WorkerName:     name,
		WorkerID:       os.Getenv("WORKER_ID"),
		WorkDir:        absWorkDir,
		CPUCount:       cpu,
		MemoryMB:       memoryMB,
		PollInterval:   poll,
		RequestTimeout: timeout,
		Heartbeat:      heartbeat,
		CleanupAfter:   cleanup,
		Capabilities:   capabilities,
		TaskRunner:     runner,
		MaxTasks:       maxTasks,
		ExitWhenIdle:   os.Getenv("EXIT_WHEN_IDLE") == "1",
	}, nil
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be a non-negative duration", name)
	}
	return parsed, nil
}
