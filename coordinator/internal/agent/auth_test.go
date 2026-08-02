package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerKeyTokenExchangesAndCaches(t *testing.T) {
	var exchanges atomic.Int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/worker-tokens/exchange" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["key"] != "scimesh_wk_live_x" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		exchanges.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"token":      "jwt-1",
			"expires_in": 100,
		})
	}))
	defer server.Close()

	provider := NewWorkerKeyToken(server.URL, "scimesh_wk_live_x", 5*time.Second)
	token, err := provider.Token()
	if err != nil || token != "jwt-1" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
	// The second call within the TTL reuses the cache.
	again, err := provider.Token()
	if err != nil || again != "jwt-1" {
		t.Fatalf("cached token = %q, err = %v", again, err)
	}
	if exchanges.Load() != 1 {
		t.Errorf("exchanges = %d, want 1", exchanges.Load())
	}
	// An explicit refresh re-exchanges.
	if err := provider.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if exchanges.Load() != 2 {
		t.Errorf("exchanges after refresh = %d, want 2", exchanges.Load())
	}
}

func TestWorkerKeyTokenRejectsBadKey(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	provider := NewWorkerKeyToken(server.URL, "bad", 5*time.Second)
	if _, err := provider.Token(); err == nil {
		t.Error("expected exchange failure for a rejected key")
	}
}

func TestNewTokenProviderSelectsStrategy(t *testing.T) {
	if _, ok := NewTokenProvider("", "", "static", time.Second).(*StaticToken); !ok {
		t.Error("expected a static token provider")
	}
	if _, ok := NewTokenProvider("key", "http://users", "", time.Second).(*WorkerKeyToken); !ok {
		t.Error("expected a worker-key provider")
	}
}

func TestClientRefreshesTokenOnceOn401(t *testing.T) {
	var attempts atomic.Int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if attempts.Load() == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	}))
	defer server.Close()

	provider := &StaticToken{token: "t"}
	client := NewClient(server.URL, provider, 5*time.Second)
	status, _, err := client.requestJSON(http.MethodGet, "/ok", map[string]any{})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d", status)
	}
	if attempts.Load() != 2 {
		t.Errorf("attempts = %d, want 2 (401 then retry)", attempts.Load())
	}
}
