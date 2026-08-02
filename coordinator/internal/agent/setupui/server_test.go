package setupui

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/agent"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer(t *testing.T, sup Supervisor) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	server := New(testLogger(), Options{
		ConfigPath:  filepath.Join(dir, "config.json"),
		Dir:         dir,
		Supervisor:  sup,
		OpenBrowser: func(string) {},
	})
	listener, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = server.Serve(ctx, listener) }()
	return server, "http://" + listener.Addr().String()
}

type fakeSup struct {
	mu      sync.Mutex
	started bool
	stopped bool
	pid     int
	alive   bool
}

func (f *fakeSup) Start(configPath, logPath string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	f.alive = true
	f.pid = 4242
	return f.pid, nil
}

func (f *fakeSup) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	f.alive = false
	return nil
}

func (f *fakeSup) Pid() int { f.mu.Lock(); defer f.mu.Unlock(); return f.pid }
func (f *fakeSup) Alive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive
}

func postJSON(t *testing.T, base, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(mustJSON(t, body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	rec := httptest.NewRecorder()
	rec.Code = resp.StatusCode
	data := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&data)
	return rec, data
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestWizardSavesConfigWithStrictPermissions(t *testing.T) {
	sup := &fakeSup{}
	server, base := newTestServer(t, sup)

	rec, _ := postJSON(t, base, "/api/config", map[string]any{
		"coordinator_url": "http://192.168.1.10:8080",
		"token":           "sm_live_secret",
		"work_dir":        "/home/emil/scimesh-worker",
		"worker_name":     "emil-laptop",
		"cpu_count":       8,
		"memory_mb":       16384,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save config: got %d, want 200", rec.Code)
	}
	info, err := os.Stat(server.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config perms = %o, want 600", perm)
	}
	config, err := agent.LoadConfigFile(server.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.CoordinatorURL != "http://192.168.1.10:8080" || config.Token != "sm_live_secret" || config.WorkDir != "/home/emil/scimesh-worker" || config.WorkerName != "emil-laptop" {
		t.Errorf("config = %+v", config)
	}
	if config.CPUCount != 8 || config.MemoryMB != 16384 {
		t.Errorf("resources: cpu=%d mem=%d", config.CPUCount, config.MemoryMB)
	}
}

func TestWizardRejectsInvalidConfig(t *testing.T) {
	sup := &fakeSup{}
	_, base := newTestServer(t, sup)

	for _, body := range []map[string]any{
		{"coordinator_url": "", "token": "x"},
		{"coordinator_url": "not-a-url", "token": "x"},
	} {
		rec, _ := postJSON(t, base, "/api/config", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %v: got %d, want 400", body, rec.Code)
		}
	}
}

func TestWizardStartStopLifecycle(t *testing.T) {
	sup := &fakeSup{}
	_, base := newTestServer(t, sup)

	// Starting without a saved config is rejected.
	rec, _ := postJSON(t, base, "/api/start", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("start without config: got %d, want 400", rec.Code)
	}

	postJSON(t, base, "/api/config", map[string]any{
		"coordinator_url": "http://127.0.0.1:8080", "token": "t", "work_dir": ".",
	})
	rec, data := postJSON(t, base, "/api/start", map[string]any{})
	if rec.Code != http.StatusOK || int(data["pid"].(float64)) != 4242 {
		t.Errorf("start: got %d %v, want 200 pid 4242", rec.Code, data)
	}
	if !sup.started {
		t.Error("supervisor never started the worker")
	}

	// Status reflects the running state.
	req, _ := http.NewRequest(http.MethodGet, base+"/api/status", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var status map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&status)
	if status["running"] != true || status["pid"] != float64(4242) {
		t.Errorf("status = %v, want running pid 4242", status)
	}

	rec, _ = postJSON(t, base, "/api/stop", map[string]any{})
	if rec.Code != http.StatusOK || !sup.stopped {
		t.Errorf("stop: got %d stopped=%v, want 200/true", rec.Code, sup.stopped)
	}
}

func TestWizardStatusPrefillsSavedConfig(t *testing.T) {
	sup := &fakeSup{}
	_, base := newTestServer(t, sup)
	postJSON(t, base, "/api/config", map[string]any{
		"coordinator_url": "http://10.0.0.5:8080", "worker_key": "smk_abc", "work_dir": "/w", "worker_name": "n1",
	})
	req, _ := http.NewRequest(http.MethodGet, base+"/api/status", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var status struct {
		ConfigPresent bool   `json:"config_present"`
		WorkerName    string `json:"worker_name"`
		Coordinator   string `json:"coordinator"`
		TokenSet      bool   `json:"token_set"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&status)
	if !status.ConfigPresent || status.WorkerName != "n1" || status.Coordinator != "http://10.0.0.5:8080" || !status.TokenSet {
		t.Errorf("status = %+v", status)
	}
	// The secret must never appear in the status projection.
	if strings.Contains(strings.ToLower(mustJSON(t, status)), "smk_abc") {
		t.Error("status leaks the worker key")
	}
}

func TestCheckCoordinatorReachable(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer stub.Close()

	report := agent.CheckCoordinator(context.Background(), stub.URL, 5*time.Second)
	if !report.Coordinator.OK {
		t.Errorf("coordinator check = %+v, want ok", report.Coordinator)
	}
}

func TestCheckCoordinatorUnreachable(t *testing.T) {
	report := agent.CheckCoordinator(context.Background(), "http://127.0.0.1:1", 2*time.Second)
	if report.Coordinator.OK {
		t.Error("unreachable coordinator reported ok")
	}
}

func TestConfigFileDefaultsAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	file := agent.ConfigFile{
		CoordinatorURL: "http://coord:8080",
		Token:          "file-token",
		WorkDir:        "/w",
		CPUCount:       4,
	}
	if err := agent.SaveConfigFile(path, file); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COORDINATOR_URL", "http://env:9090")
	t.Setenv("WORKER_AUTH_TOKEN", "")
	config, err := agent.LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.CoordinatorURL != "http://env:9090" {
		t.Errorf("env must win: %s", config.CoordinatorURL)
	}
	if config.Token != "file-token" {
		t.Errorf("token = %q, want the file value", config.Token)
	}
	if config.CPUCount != 4 {
		t.Errorf("cpu = %d", config.CPUCount)
	}
}
