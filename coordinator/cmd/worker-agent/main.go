// Command worker-agent is the Go worker agent: a coordinator client that
// executes SDK workloads in a Python subprocess per claimed task.
package main

import (
	"log/slog"
	"os"

	"github.com/emil28092005/SciMesh/coordinator/internal/agent"
)

func main() {
	config, err := agent.LoadConfig()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := agent.NewClient(config.CoordinatorURL, config.Token, config.RequestTimeout)
	runner := agent.NewTaskRunner(config.TaskRunner)
	daemon := agent.NewDaemon(config, client, runner, logger)
	if err := daemon.RunForever(); err != nil {
		logger.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}
