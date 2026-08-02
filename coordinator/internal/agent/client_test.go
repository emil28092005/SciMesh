package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	return NewClient(server.URL, "test-token", 5*time.Second)
}

func TestClientRegisterClaimHeartbeat(t *testing.T) {
	var registered, claimed, heartbeated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/workers/register":
			registered = true
			writeJSON(w, http.StatusCreated, map[string]any{
				"worker_id":                  "22222222-2222-4222-8222-222222222222",
				"heartbeat_interval_seconds": 15,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/claim":
			claimed = true
			writeJSON(w, http.StatusOK, validTaskPayload())
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/11111111-1111-4111-8111-111111111111/heartbeat":
			heartbeated = true
			writeJSON(w, http.StatusOK, map[string]any{
				"lease_expires_at": time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server)

	registeredWorker, err := client.Register("test-worker", []string{"similarity-search"}, 2, 1024)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if registeredWorker.WorkerID != "22222222-2222-4222-8222-222222222222" {
		t.Errorf("worker id = %q", registeredWorker.WorkerID)
	}

	task, err := client.Claim("22222222-2222-4222-8222-222222222222", []string{"similarity-search"})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if task == nil || task.Workload != "similarity-search" {
		t.Fatalf("claim = %+v", task)
	}

	renewed, err := client.Heartbeat(task, "22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if renewed.Before(time.Now()) {
		t.Error("renewed lease is in the past")
	}
	if !registered || !claimed || !heartbeated {
		t.Error("some endpoints were not hit")
	}
}

func TestClientClaimEmptyAndConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tasks/claim":
			w.WriteHeader(http.StatusNoContent)
		case "/tasks/11111111-1111-4111-8111-111111111111/heartbeat":
			w.WriteHeader(http.StatusConflict)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server)

	task, err := client.Claim("worker", []string{"similarity-search"})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if task != nil {
		t.Error("expected no task for 204")
	}

	claimed, err := ParseTask(validTaskPayload())
	if err != nil {
		t.Fatalf("ParseTask: %v", err)
	}
	if _, err := client.Heartbeat(claimed, "worker"); err == nil {
		t.Error("expected conflict error")
	} else if _, ok := err.(*ConflictError); !ok {
		t.Errorf("error type = %T", err)
	}
}

func TestClientUploadSubmitFail(t *testing.T) {
	var uploadedPath string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/tasks/11111111-1111-4111-8111-111111111111/artifacts/"):
			if r.Header.Get("X-Worker-ID") != "worker" || r.Header.Get("X-Task-Attempt") != "1" {
				t.Errorf("missing identity headers: %+v", r.Header)
			}
			uploadedPath = r.URL.Path
			writeJSON(w, http.StatusOK, map[string]any{
				"artifact_id": "33333333-3333-4333-8333-333333333333",
				"uri":         server.URL + "/artifacts/333/download",
				"sha256":      sha256Of(t, "partial body"),
				"size_bytes":  int64(len("partial body")),
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/result"):
			writeJSON(w, http.StatusAccepted, map[string]any{})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/failure"):
			writeJSON(w, http.StatusAccepted, map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server)
	task, _ := ParseTask(validTaskPayload())

	dir := t.TempDir()
	partial := filepath.Join(dir, "result.csv")
	if err := os.WriteFile(partial, []byte("partial body"), 0o644); err != nil {
		t.Fatal(err)
	}
	uploaded, err := client.Upload(task, "worker", partial, "text/csv")
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if uploaded.SizeBytes != int64(len("partial body")) {
		t.Errorf("size = %d", uploaded.SizeBytes)
	}
	if !strings.Contains(uploadedPath, "result.csv") {
		t.Errorf("upload path = %q", uploadedPath)
	}

	if err := client.Submit(task, "worker", uploaded, map[string]any{"rows": 1}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := client.Fail(task, "worker", "ValueError", "bad input", false); err != nil {
		t.Fatalf("Fail: %v", err)
	}
}

func TestClientDownloadVerifiesChecksumAndStripsAuthOnRedirect(t *testing.T) {
	var redirectedAuth string
	var bucket *httptest.Server
	bucket = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("input bytes"))
	}))
	defer bucket.Close()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tasks/11111111-1111-4111-8111-111111111111/input" {
			http.Redirect(w, r, bucket.URL+"/presigned", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	destination := filepath.Join(t.TempDir(), "input")
	digest, err := client.Download(server.URL+"/tasks/11111111-1111-4111-8111-111111111111/input", destination)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if digest != sha256Of(t, "input bytes") {
		t.Errorf("digest = %q", digest)
	}
	if redirectedAuth != "" {
		t.Error("Authorization must be stripped on the redirected download")
	}
}

func TestSanitizeErrorMessageRedactsPaths(t *testing.T) {
	message := SanitizeErrorMessage(
		"failed at /home/alice/work/attempts/1/input and /private/secret.txt",
		"/home/alice/work",
	)
	for _, forbidden := range []string{"/home/alice", "/private/secret.txt"} {
		if strings.Contains(message, forbidden) {
			t.Errorf("message leaks %q: %q", forbidden, message)
		}
	}
	if !strings.Contains(message, "<worker-dir>") {
		t.Errorf("work dir not redacted: %q", message)
	}
	long := SanitizeErrorMessage(strings.Repeat("x", 500), "/tmp")
	if len(long) != 300 {
		t.Errorf("truncated length = %d", len(long))
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func sha256Of(t *testing.T, value string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest)
}
