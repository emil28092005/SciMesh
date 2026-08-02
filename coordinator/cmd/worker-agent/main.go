// Command worker-agent is the Go worker agent: a coordinator client that
// executes SDK workloads in a Python subprocess per claimed task. It also
// carries the local setup wizard (`worker-agent setup`) so a machine that
// installs only the worker can configure and start itself without a
// coordinator on site.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/agent"
	"github.com/emil28092005/SciMesh/coordinator/internal/agent/setupui"
)

// version is injected at build time (-ldflags "-X main.version=...") and
// reported by --version. "dev" marks a local build.
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		os.Exit(runSetup(os.Args[2:]))
	}

	fs := flag.NewFlagSet("worker-agent", flag.ExitOnError)
	showVersion := fs.Bool("version", false, "print the build version and exit")
	configPath := fs.String("config", "", "path to a JSON config file (SCIMESH_WORKER_CONFIG overrides the default)")
	checkMode := fs.Bool("check", false, "run the preflight check (coordinator + local runtime) and exit 0/1")
	checkURL := fs.String("coordinator-url", "", "coordinator URL to probe in --check mode")
	_ = fs.Parse(os.Args[1:])

	agent.Version = version

	if *showVersion {
		fmt.Println("worker-agent " + version)
		return
	}

	if *checkMode {
		url := *checkURL
		if url == "" {
			url = os.Getenv("COORDINATOR_URL")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if url == "" {
			fmt.Println("check: no coordinator URL (pass --coordinator-url or set COORDINATOR_URL)")
			os.Exit(1)
		}
		report := agent.RunCheck(ctx, url)
		printCheck(report)
		if !report.Coordinator.OK || !report.Python.OK || !report.Scimesh.OK {
			os.Exit(1)
		}
		return
	}

	config, err := loadConfig(*configPath)
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

// loadConfig prefers a --config file; environment variables override the file
// (see agent.ConfigFile.Config). Without a file, the plain environment path is
// used exactly as before.
func loadConfig(configPath string) (*agent.Config, error) {
	if configPath != "" {
		return agent.LoadConfigFile(configPath)
	}
	envPath := os.Getenv("SCIMESH_WORKER_CONFIG")
	if envPath != "" {
		if _, err := os.Stat(envPath); err == nil { //nolint:gosec // G703: path is the operator's own env var
			return agent.LoadConfigFile(envPath)
		}
	}
	return agent.LoadConfig()
}

func printCheck(report agent.CheckReport) {
	fmt.Printf("worker-agent %s\n", report.Agent)
	line := func(item agent.CheckItem) string {
		mark := "✗"
		if item.OK {
			mark = "✓"
		}
		detail := item.Detail
		if item.Latency > 0 {
			detail = fmt.Sprintf("%s (%d ms)", detail, item.Latency)
		}
		return fmt.Sprintf("  %s %s: %s", mark, item.Name, detail)
	}
	fmt.Println(line(report.Coordinator))
	fmt.Println(line(report.Auth))
	fmt.Println(line(report.Python))
	fmt.Println(line(report.Scimesh))
}

// runSetup serves the local setup wizard until interrupted. It binds the
// loopback interface only.
func runSetup(args []string) int {
	fs := flag.NewFlagSet("worker-agent setup", flag.ContinueOnError)
	port := fs.Int("port", 0, "listen port (default 12700)")
	noOpen := fs.Bool("no-open", false, "do not open the browser automatically")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	server := setupui.New(logger, setupui.Options{
		Port: *port,
		OpenBrowser: func(url string) {
			if *noOpen {
				return
			}
			openBrowser(url)
		},
	})
	listener, err := server.Listen()
	if err != nil {
		logger.Error("setup wizard could not bind the loopback port", "err", err)
		return 1
	}
	url := "http://" + listener.Addr().String()
	logger.Info("SciMesh worker setup wizard", "url", url, "press-ctrl-c-to-stop", true)
	server.OpenBrowser(url)
	// Block until the signal arrives (never returns an error that matters: a
	// cancelled context is the normal exit path).
	err = server.Serve(ctx, listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("setup wizard stopped", "err", err)
		return 1
	}
	return 0
}

// openBrowser points the user's default browser at the wizard. Best-effort:
// a missing browser must never fail the setup flow.
func openBrowser(url string) {
	for _, candidate := range [][]string{
		{"xdg-open", url},
		{"open", url},
		{"cmd", "/c", "start", url},
	} {
		binary, err := exec.LookPath(candidate[0])
		if err != nil {
			continue
		}
		//nolint:gosec // G204: candidates are our own fixed list; the url is a loopback literal
		_ = exec.CommandContext(context.Background(), binary, candidate[1:]...).Start()
		return
	}
}
