package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkerKeyCreateProxiesWithBody(t *testing.T) {
	var gotAuth, gotPath, gotMethod, gotBody string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotMethod = r.Header.Get("Authorization"), r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"11111111-1111-1111-1111-111111111111","name":"box","prefix":"scimesh_wk_live_ab","created_at":"2026-07-26T00:00:00Z","key":"scimesh_wk_live_secret"}`))
	}))
	defer stub.Close()
	s := newLoginServer(stub)

	req := newReq(http.MethodPost, "/ui/api/worker-keys", strings.NewReader(`{"name":"box"}`))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "my.jwt"})
	rec := httptest.NewRecorder()
	s.handleUIWorkerKeyCreate(rec, req)

	if gotAuth != "Bearer my.jwt" || gotPath != "/worker-keys" || gotMethod != http.MethodPost {
		t.Fatalf("proxy: auth=%q path=%q method=%q", gotAuth, gotPath, gotMethod)
	}
	if !strings.Contains(gotBody, `"name":"box"`) {
		t.Errorf("request body not forwarded: %q", gotBody)
	}
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "scimesh_wk_live_secret") {
		t.Errorf("response not passed through: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWorkerKeysListProxies(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/worker-keys" {
			t.Errorf("unexpected upstream call %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"worker_keys":[{"id":"1","name":"box","prefix":"scimesh_wk_live_ab","created_at":"2026-07-26T00:00:00Z"}]}`))
	}))
	defer stub.Close()
	s := newLoginServer(stub)

	req := newReq(http.MethodGet, "/ui/api/worker-keys", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "my.jwt"})
	rec := httptest.NewRecorder()
	s.handleUIWorkerKeysList(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "worker_keys") {
		t.Errorf("list not passed through: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWorkerKeyRevokeProxiesDelete(t *testing.T) {
	const id = "22222222-2222-2222-2222-222222222222"
	var gotPath, gotMethod string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer stub.Close()
	s := newLoginServer(stub)

	req := newReq(http.MethodPost, "/ui/api/worker-keys/"+id+"/revoke", nil)
	req.SetPathValue("id", id)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "my.jwt"})
	rec := httptest.NewRecorder()
	s.handleUIWorkerKeyRevoke(rec, req)

	if gotMethod != http.MethodDelete || gotPath != "/worker-keys/"+id {
		t.Fatalf("proxy: method=%q path=%q", gotMethod, gotPath)
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("revoke status = %d, want 204", rec.Code)
	}
}

func TestWorkerKeyRevokeRejectsBadID(t *testing.T) {
	s := newLoginServer(httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("must not call userservice for an invalid id")
	})))
	req := newReq(http.MethodPost, "/ui/api/worker-keys/not-a-uuid/revoke", nil)
	req.SetPathValue("id", "not-a-uuid")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "my.jwt"})
	rec := httptest.NewRecorder()
	s.handleUIWorkerKeyRevoke(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: got %d, want 400", rec.Code)
	}
}

func TestWorkerKeysRequireSession(t *testing.T) {
	s := newLoginServer(httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("must not call userservice without a session cookie")
	})))
	rec := httptest.NewRecorder()
	s.handleUIWorkerKeysList(rec, newReq(http.MethodGet, "/ui/api/worker-keys", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no cookie: got %d, want 401", rec.Code)
	}
}
