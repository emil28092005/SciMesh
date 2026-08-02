package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sha256HexOf(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum)
}

// fakeRunnerScript writes a result manifest for --output and exits with the
// given code.
func fakeRunnerScript(t *testing.T, dir string, exitCode int) string {
	t.Helper()
	script := filepath.Join(dir, "fake-runner.sh")
	content := `#!/bin/sh
out=""
task_dir=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) out="$2"; shift 2;;
    --task-dir) task_dir="$2"; shift 2;;
    *) shift;;
  esac
done
printf 'id,score\n' > "$task_dir/result.csv"
printf '{"artifact_path":"%s/result.csv","content_type":"text/csv","metrics":{"rows":1}}' "$task_dir" > "$out"
exit ` + fmt.Sprint(exitCode) + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// fakeCoordinator implements the v1 contract over HTTP and records calls.
type fakeCoordinator struct {
	server     *httptest.Server
	task       map[string]any
	submits    []map[string]any
	failures   []map[string]any
	heartbeats int
	uploadSHA  string
	uploadSize int64
	inputBytes []byte
	conflict   bool // 409 on heartbeat/upload/result
}

func newFakeCoordinator(t *testing.T, task map[string]any) *fakeCoordinator {
	t.Helper()
	fake := &fakeCoordinator{task: task, inputBytes: []byte("input fixture")}
	fake.uploadSHA = sha256HexOf(string(fake.inputBytes))
	fake.uploadSize = int64(len(fake.inputBytes))
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/workers/register":
			writeJSON(w, http.StatusCreated, map[string]any{
				"worker_id":                  "22222222-2222-4222-8222-222222222222",
				"heartbeat_interval_seconds": 15.0,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/claim":
			if fake.task == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeJSON(w, http.StatusOK, fake.task)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/input"):
			_, _ = w.Write(fake.inputBytes)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/heartbeat"):
			fake.heartbeats++
			if fake.conflict {
				w.WriteHeader(http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"lease_expires_at": time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
			})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/artifacts/"):
			raw, _ := io.ReadAll(r.Body)
			fake.uploadSize = int64(len(raw))
			fake.uploadSHA = fmt.Sprintf("%x", sha256.Sum256(raw))
			if fake.conflict {
				w.WriteHeader(http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"artifact_id": "33333333-3333-4333-8333-333333333333",
				"uri":         server.URL + "/artifacts/333/download",
				"sha256":      fake.uploadSHA,
				"size_bytes":  fake.uploadSize,
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/result"):
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			fake.submits = append(fake.submits, payload)
			if fake.conflict {
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/failure"):
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			fake.failures = append(fake.failures, payload)
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	fake.server = server
	return fake
}

func (f *fakeCoordinator) close() { f.server.Close() }

func testDaemon(t *testing.T, fake *fakeCoordinator, script string) *Daemon {
	t.Helper()
	config := &Config{
		CoordinatorURL: fake.server.URL,
		WorkerName:     "test-worker",
		WorkerID:       "22222222-2222-4222-8222-222222222222",
		WorkDir:        t.TempDir(),
		CPUCount:       1,
		PollInterval:   time.Millisecond,
		RequestTimeout: 5 * time.Second,
		Heartbeat:      15 * time.Second,
		Capabilities:   []string{"similarity-search"},
		TaskRunner:     []string{script},
	}
	client := NewClient(fake.server.URL, &StaticToken{token: "test-token"}, 5*time.Second)
	runner := NewTaskRunner(config.TaskRunner)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	daemon := NewDaemon(config, client, runner, logger)
	if err := daemon.register(); err != nil {
		t.Fatalf("register: %v", err)
	}
	return daemon
}

func validClaimedTaskPayload() map[string]any {
	return map[string]any{
		"task_id":          "11111111-1111-4111-8111-111111111111",
		"attempt":          1.0,
		"lease_expires_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
		"workload":         "similarity-search",
		"input": map[string]any{
			"uri":    "/tasks/11111111-1111-4111-8111-111111111111/input",
			"sha256": sha256HexOf("input fixture"),
		},
		"parameters": map[string]any{"query_smiles": "CCO"},
	}
}

func TestDaemonCompletesAClaimedTask(t *testing.T) {
	fake := newFakeCoordinator(t, validClaimedTaskPayload())
	defer fake.close()
	daemon := testDaemon(t, fake, fakeRunnerScript(t, t.TempDir(), 0))

	outcome, err := daemon.runOnce()
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if !outcome.Claimed || !outcome.Completed {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(fake.submits) != 1 {
		t.Fatalf("submits = %d", len(fake.submits))
	}
	result := fake.submits[0]["result"].(map[string]any)
	if result["artifact_id"] != "33333333-3333-4333-8333-333333333333" {
		t.Errorf("result artifact = %v", result)
	}
	metrics := fake.submits[0]["metrics"].(map[string]any)
	if metrics["rows"] != float64(1) {
		t.Errorf("metrics = %v", metrics)
	}
	if _, ok := metrics["elapsed_seconds"].(float64); !ok {
		t.Errorf("missing elapsed_seconds: %v", metrics)
	}
	if fake.heartbeats < 1 {
		t.Error("expected at least one heartbeat")
	}
	if len(fake.failures) != 0 {
		t.Errorf("unexpected failures: %v", fake.failures)
	}
}

func TestDaemonReportsChecksumMismatchAsPermanentFailure(t *testing.T) {
	payload := validClaimedTaskPayload()
	payload["input"].(map[string]any)["sha256"] = strings.Repeat("b", 64)
	fake := newFakeCoordinator(t, payload)
	defer fake.close()
	daemon := testDaemon(t, fake, fakeRunnerScript(t, t.TempDir(), 0))

	outcome, err := daemon.runOnce()
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if outcome.Completed {
		t.Fatal("task must not complete on checksum mismatch")
	}
	if len(fake.failures) != 1 {
		t.Fatalf("failures = %d", len(fake.failures))
	}
	failure := fake.failures[0]
	if failure["error_code"] != "ValueError" || failure["retryable"] != false {
		t.Errorf("failure = %v", failure)
	}
	if !strings.Contains(failure["error_message"].(string), "checksum") {
		t.Errorf("message = %v", failure["error_message"])
	}
	if len(fake.submits) != 0 {
		t.Error("no submission expected")
	}
}

func TestDaemonReportsPermanentRunnerFailure(t *testing.T) {
	fake := newFakeCoordinator(t, validClaimedTaskPayload())
	defer fake.close()
	daemon := testDaemon(t, fake, fakeRunnerScript(t, t.TempDir(), ExitPermanent))

	outcome, err := daemon.runOnce()
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if outcome.Completed {
		t.Fatal("task must not complete")
	}
	if len(fake.failures) != 1 || fake.failures[0]["retryable"] != false {
		t.Fatalf("failures = %v", fake.failures)
	}
}

func TestDaemonReportsRetryableRunnerFailure(t *testing.T) {
	fake := newFakeCoordinator(t, validClaimedTaskPayload())
	defer fake.close()
	daemon := testDaemon(t, fake, fakeRunnerScript(t, t.TempDir(), 1))

	outcome, err := daemon.runOnce()
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if outcome.Completed {
		t.Fatal("task must not complete")
	}
	if len(fake.failures) != 1 || fake.failures[0]["retryable"] != true {
		t.Fatalf("failures = %v", fake.failures)
	}
}

func TestDaemonLeaseConflictStopsWithoutFailureReport(t *testing.T) {
	fake := newFakeCoordinator(t, validClaimedTaskPayload())
	fake.conflict = true
	defer fake.close()
	daemon := testDaemon(t, fake, fakeRunnerScript(t, t.TempDir(), 0))

	outcome, err := daemon.runOnce()
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if !outcome.Claimed {
		t.Fatal("task was claimed")
	}
	if len(fake.failures) != 0 {
		t.Errorf("no failure report expected after lease loss: %v", fake.failures)
	}
	if len(fake.submits) != 0 {
		t.Errorf("no submission expected after lease loss: %v", fake.submits)
	}
}

func TestDaemonIdleClaimIsNotCompleted(t *testing.T) {
	fake := newFakeCoordinator(t, nil)
	defer fake.close()
	daemon := testDaemon(t, fake, fakeRunnerScript(t, t.TempDir(), 0))

	outcome, err := daemon.runOnce()
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if outcome.Claimed || outcome.Completed {
		t.Fatalf("outcome = %+v", outcome)
	}
}
