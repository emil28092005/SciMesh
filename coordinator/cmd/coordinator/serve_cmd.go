package main

import (
	"context"

	"github.com/emil28092005/SciMesh/coordinator/internal/agent"

	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/infra"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice"
)

// runServe implements `coordinator serve`: the single-binary mode for a
// scientist. It provisions a data directory (default ~/.scimesh) with the
// coordinator and userservice sqlite databases, secrets, the admin account,
// and optionally local worker agents — then runs the same server run() does.
func runServe(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.Usage = func() {
		_, _ = fmt.Fprintf(flags.Output(), "usage: coordinator serve [options]\n")
		_, _ = fmt.Fprintf(flags.Output(), "Runs the whole SciMesh platform from one binary: embedded databases, the\n")
		_, _ = fmt.Fprintf(flags.Output(), "userservice, and optional local workers. No PostgreSQL or Docker needed.\n\n")
		flags.PrintDefaults()
	}
	var (
		dataDir   = flags.String("data-dir", defaultDataDir(), "data directory (default: ~/.scimesh)")
		addr      = flags.String("addr", "127.0.0.1:8080", "listen address")
		workers   = flags.Int("workers", 1, "number of local worker agents to spawn")
		open      = flags.Bool("open", false, "open the UI in the browser")
		docsDir   = flags.String("docs-dir", "", "built MkDocs site directory to serve at /ui/docs/")
		email     = flags.String("admin-email", "admin@scimesh.local", "admin account email")
		password  = flags.String("admin-password", "", "admin password (generated on first run when empty)")
		publicURL = flags.String("public-url", "", "browser/worker-facing coordinator URL (default: http://<addr>)")
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("serve takes no positional arguments")
	}
	if *workers < 0 {
		return fmt.Errorf("--workers must be >= 0")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := os.MkdirAll(*dataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// 1. Secrets, persisted in the data dir so restarts keep working.
	jwtSecret, err := loadOrGenerate(filepath.Join(*dataDir, "jwt.secret"))
	if err != nil {
		return err
	}
	workerToken, err := loadOrGenerate(filepath.Join(*dataDir, "worker.token"))
	if err != nil {
		return err
	}

	// 2. Admin account: generated once and printed, remembered for later boots.
	if *password == "" {
		*password, err = loadOrGenerate(filepath.Join(*dataDir, "admin.password"))
		if err != nil {
			return err
		}
	}

	// 3. Scientific runtime: ensure the managed venv (best effort).
	venvPython := filepath.Join(*dataDir, "venv", binName("bin/python"))
	ensureRuntime(log, *dataDir, venvPython)

	// 4. Embedded userservice on the loopback interface.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	usersAddr, closeUsers, err := userservice.Serve(ctx, userservice.Config{
		DBPath:        filepath.Join(*dataDir, "users.db"),
		JWTSecret:     jwtSecret,
		AdminEmail:    *email,
		AdminPassword: *password,
		Log:           log,
	})
	if err != nil {
		return fmt.Errorf("embedded userservice: %w", err)
	}
	defer func() { _ = closeUsers() }()

	// 5. Local worker agents before the server, so they can claim immediately.
	// They always dial the loopback address: an --addr of 0.0.0.0 is not a
	// connectable target from the same host.
	agentURL, resolvedPublic := serveURLs(*addr, *publicURL)
	agents, err := spawnAgents(ctx, log, *dataDir, *workers, agentURL, workerToken, venvPython)
	if err != nil {
		return err
	}
	defer stopAgents(agents)

	// 6. The coordinator server itself.
	cfg := infra.Config{
		Addr:                 *addr,
		DatabaseEngine:       "sqlite",
		DBPath:               filepath.Join(*dataDir, "scimesh.db"),
		Token:                workerToken,
		JWTSecret:            jwtSecret,
		UserserviceURL:       "http://" + usersAddr,
		PublicCoordinatorURL: resolvedPublic,
		// The exchange is fronted by the coordinator's own proxy, so the UI
		// falls back to the coordinator origin for USERSERVICE_URL.
		PublicUserserviceURL: "",
		LogLevel:             "info",
		StorageDir:           filepath.Join(*dataDir, "artifacts"),
		DocsDir:              *docsDir,
		MaxUploadBytes:       1 << 30,
		DBMaxConns:           4,
		DBConnectTimeout:     10 * time.Second,
		RequestTimeout:       15 * time.Second,
		HeartbeatInterval:    15 * time.Second,
		LeaseDuration:        2 * time.Minute,
		DefaultMaxAttempts:   3,
		QuorumSize:           2,
		ReaperInterval:       30 * time.Second,
		WorkerOfflineAfter:   1 * time.Minute,
		AutoMigrate:          true,
	}
	browserURL, _ := serveURLs(*addr, *publicURL)
	if *open {
		openBrowser(browserURL + "/ui/admin")
	}

	// Print the login once the server is about to start.
	fmt.Printf("\nSciMesh is starting at %s/ui\n", browserURL)
	fmt.Printf("  admin login: %s / %s\n", *email, *password)
	if runtimeStatus(venvPython) {
		fmt.Printf("  scientific runtime: ready (%s)\n", venvPython)
	} else {
		fmt.Printf("  scientific runtime: NOT ready — install Python 3, then restart serve\n")
	}
	fmt.Printf("  data directory: %s\n\n", *dataDir)

	err = runWithConfig(cfg)
	cancel()
	stopAgents(agents)
	return err
}

// defaultDataDir returns the platform-appropriate data directory.
func defaultDataDir() string {
	if dir := os.Getenv("SCIMESH_DATA_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".scimesh"
	}
	return filepath.Join(home, ".scimesh")
}

// loadOrGenerate reads a secret file, creating it with fresh random content
// (chmod 0600) when missing.
// #nosec G304 -- the path is an operator-supplied secret file inside the data dir.
func loadOrGenerate(path string) (string, error) {
	if raw, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(raw)), nil
	}
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(buffer)
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return secret, nil
}

// spawnAgents starts `coordinator agent` subprocesses that claim tasks from
// the coordinator. Each gets its own work directory under the data dir.
func spawnAgents(ctx context.Context, log *slog.Logger, dataDir string, count int,
	coordinatorURL, token, venvPython string) ([]*exec.Cmd, error) {

	var agents []*exec.Cmd
	for i := 0; i < count; i++ {
		workDir := filepath.Join(dataDir, "workers", fmt.Sprintf("%d", i))
		if err := os.MkdirAll(workDir, 0o750); err != nil {
			return agents, err
		}
		taskRunner := defaultTaskRunner(venvPython)
		// #nosec G204,G702 -- the command is this binary itself with operator flags.
		cmd := exec.CommandContext(ctx, os.Args[0], "agent",
			"--coordinator-url", coordinatorURL,
			"--token", token,
			"--work-dir", workDir,
			"--task-runner", taskRunner,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return agents, fmt.Errorf("start local agent %d: %w", i, err)
		}
		agents = append(agents, cmd)
		log.Info("local worker agent started", "index", i)
	}
	return agents, nil
}

// stopAgents terminates the spawned agents and waits briefly for them.
func stopAgents(agents []*exec.Cmd) {
	for _, agent := range agents {
		if agent.Process != nil {
			_ = agent.Process.Kill()
		}
	}
	done := make(chan struct{})
	go func() {
		for _, agent := range agents {
			_, _ = agent.Process.Wait()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// defaultTaskRunner picks the managed venv python when present, else the
// system `python`.
func defaultTaskRunner(venvPython string) string {
	if runtimeStatus(venvPython) {
		return venvPython + " -I -m scimesh.worker.task"
	}
	return "python -I -m scimesh.worker.task"
}

// ensureRuntime creates the managed venv and installs scimesh into it, unless
// it already exists. Best effort: a missing Python only logs a hint.
func ensureRuntime(log *slog.Logger, dataDir, venvPython string) {
	if runtimeStatus(venvPython) {
		return
	}
	python := findPython()
	if python == "" {
		log.Warn("python3 not found; local workers need it to run scientific workloads")
		return
	}
	log.Info("creating the scientific runtime venv", "python", python)
	venvDir := filepath.Dir(filepath.Dir(venvPython))
	// #nosec G204 -- python comes from PATH and venvDir from the data dir.
	create := exec.CommandContext(context.Background(), python, "-m", "venv", venvDir)
	if out, err := create.CombinedOutput(); err != nil {
		log.Warn("venv creation failed; local workers need a manual Python install", "err", err, "output", string(out))
		return
	}
	pip := filepath.Join(venvDir, binName("bin/pip"))
	// The scimesh package is installed from an explicit source only: the PyPI
	// name belongs to an unrelated project, so `pip install scimesh` would
	// fetch a stranger's package. Default: download the wheel attached to our
	// own GitHub release for this binary version; SCIMESH_PIP_PACKAGE
	// overrides with a custom wheel, checkout or index.
	source := os.Getenv("SCIMESH_PIP_PACKAGE")
	if source == "" {
		url, _, err := agent.ReleaseWheelURL(version)
		if err != nil {
			log.Warn("scientific runtime venv created, but scimesh is not installed",
				"hint", "set SCIMESH_PIP_PACKAGE to your wheel or index")
			return
		}
		downloaded, err := agent.DownloadWheel(context.Background(), url, venvDir)
		if err != nil {
			log.Warn("could not download the scimesh wheel for this release",
				"err", err, "hint", "set SCIMESH_PIP_PACKAGE to your wheel or index")
			return
		}
		source = downloaded
	}
	// #nosec G204,G702 -- pip and source are operator-configured paths.
	install := exec.CommandContext(context.Background(), pip, "install", source)
	if out, err := install.CombinedOutput(); err != nil {
		log.Warn("pip install failed", "err", err, "output", string(out))
		return
	}
	log.Info("scientific runtime installed", "venv", venvDir)
}

// findPython locates a usable python3.
func findPython() string {
	for _, candidate := range []string{"python3", "python"} {
		path, err := exec.LookPath(candidate)
		if err == nil {
			return path
		}
	}
	return ""
}

// runtimeStatus reports whether the managed venv python exists.
func runtimeStatus(venvPython string) bool {
	info, err := os.Stat(venvPython)
	return err == nil && !info.IsDir()
}

// binName adapts a relative path to the platform layout.
func binName(relative string) string {
	if runtime.GOOS == "windows" {
		parts := strings.Split(relative, "/")
		parts[len(parts)-1] += ".exe"
		return strings.Join(parts, string(filepath.Separator))
	}
	return relative
}

// openBrowser opens the UI in the platform's default browser.
func openBrowser(target string) {
	command := ""
	switch runtime.GOOS {
	case "darwin":
		command = "open"
	case "windows":
		command = "rundll32"
	default:
		command = "xdg-open"
	}
	if command == "rundll32" {
		// #nosec G204 -- target is the local UI URL the operator asked to open.
		_ = exec.CommandContext(context.Background(), "rundll32", "url.dll,FileProtocolHandler", target).Start()
		return
	}
	// #nosec G204 -- target is the local UI URL the operator asked to open.
	_ = exec.CommandContext(context.Background(), command, target).Start()
}

// serveURLs derives the two addresses of a serve instance from the listen
// address and the optional --public-url flag:
//
//   - the agent URL is always the loopback form of the port, because spawned
//     local workers share the host and 0.0.0.0 is not connectable from it;
//   - the public URL is what browsers and remote workers are told. An explicit
//     --public-url wins; a listen host that is a real address is used as-is;
//     a wildcard host (0.0.0.0, ::, or empty) yields an empty public URL, so
//     the UI falls back to the browser's own origin (the coordinator's LAN
//     address as the browser sees it).
func serveURLs(addr, publicURL string) (agentURL, resolvedPublic string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port in the listen address: assume the default and treat the
		// whole string as a host (e.g. a bare wildcard).
		host, port = addr, "8080"
	}
	agentURL = "http://127.0.0.1:" + port
	if publicURL != "" {
		return agentURL, publicURL
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "", "0.0.0.0", "::":
		return agentURL, ""
	default:
		return agentURL, "http://" + addr
	}
}
