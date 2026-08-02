// Command worker-agent is the Go worker agent: a coordinator client that
// executes SDK workloads in a Python subprocess per claimed task.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/emil28092005/SciMesh/coordinator/internal/agent"
)

// version is injected at build time (-ldflags "-X main.version=...") and
// reported by --version. "dev" marks a local build.
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("worker-agent " + version)
		return
	}
	config, err := agent.LoadConfig()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	tokens := agent.NewTokenProvider(
		config.WorkerKey,
		config.UserserviceURL,
		config.Token,
		config.RequestTimeout,
	)
	client := agent.NewClient(config.CoordinatorURL, tokens, config.RequestTimeout)
	runner := agent.NewTaskRunner(config.TaskRunner)
	daemon := agent.NewDaemon(config, client, runner, logger)
	if err := daemon.RunForever(); err != nil {
		logger.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}
