package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/agent"
)

// runAgent implements `coordinator agent`: the worker agent as a subcommand of
// the same binary, so one file can serve the whole platform. `serve` spawns
// these for its local workers.
func runAgent(args []string) error {
	flags := flag.NewFlagSet("agent", flag.ContinueOnError)
	flags.Usage = func() {
		_, _ = fmt.Fprintf(flags.Output(), "usage: coordinator agent [options]\n")
		_, _ = fmt.Fprintf(flags.Output(), "Runs as a worker agent: claims tasks, executes SDK workloads in a\n")
		_, _ = fmt.Fprintf(flags.Output(), "Python subprocess, uploads results.\n\n")
		flags.PrintDefaults()
	}
	var (
		coordinatorURL = flags.String("coordinator-url", os.Getenv("COORDINATOR_URL"), "coordinator base URL")
		token          = flags.String("token", os.Getenv("WORKER_AUTH_TOKEN"), "worker bearer token")
		workDir        = flags.String("work-dir", os.Getenv("WORK_DIR"), "worker work directory")
		name           = flags.String("name", os.Getenv("WORKER_NAME"), "worker name (default: hostname)")
		workerID       = flags.String("worker-id", os.Getenv("WORKER_ID"), "persistent worker id (optional)")
		cpuCount       = flags.Int("cpu", envInt("CPU_COUNT", 1), "advertised CPU cores")
		memoryMB       = flags.Int("memory-mb", envInt("MEMORY_MB", 1024), "advertised memory in MiB")
		poll           = flags.Duration("poll-interval", 2*time.Second, "claim poll interval")
		taskRunner     = flags.String("task-runner", os.Getenv("TASK_RUNNER"), "python command + args that run scimesh.worker.task")
		maxTasks       = flags.Int("max-tasks", envInt("MAX_TASKS", 0), "stop after N completed tasks (0 = unlimited)")
		exitWhenIdle   = flags.Bool("exit-when-idle", false, "exit when the queue is empty")
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("agent takes no positional arguments")
	}
	if *coordinatorURL == "" || *token == "" || *workDir == "" {
		return fmt.Errorf("--coordinator-url, --token, and --work-dir are required")
	}
	if *taskRunner == "" {
		*taskRunner = "python -I -m scimesh.worker.task"
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	config := agent.Config{
		CoordinatorURL: strings.TrimRight(*coordinatorURL, "/"),
		Capabilities:   agent.DefaultCapabilities(),
		Token:          *token,
		WorkerName:     *name,
		WorkerID:       *workerID,
		WorkDir:        *workDir,
		CPUCount:       *cpuCount,
		MemoryMB:       *memoryMB,
		PollInterval:   *poll,
		RequestTimeout: 30 * time.Second,
		Heartbeat:      15 * time.Second,
		TaskRunner:     strings.Fields(*taskRunner),
		MaxTasks:       *maxTasks,
		ExitWhenIdle:   *exitWhenIdle,
	}
	tokens := agent.NewTokenProvider("", "", config.Token, config.RequestTimeout)
	client := agent.NewClient(config.CoordinatorURL, tokens, config.RequestTimeout)
	runner := agent.NewTaskRunner(config.TaskRunner)
	daemon := agent.NewDaemon(&config, client, runner, logger)
	return daemon.RunForever()
}

func envInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
		return fallback
	}
	return n
}
