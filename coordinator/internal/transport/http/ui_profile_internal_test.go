package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProfileProxiesMe(t *testing.T) {
	var gotAuth, gotPath string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"11111111-1111-1111-1111-111111111111","email":"me@example.com","role":"user","verified":false,"created_at":"2026-07-26T00:00:00Z"}`))
	}))
	defer stub.Close()
	s := newLoginServer(stub)

	req := newReq(http.MethodGet, "/ui/profile", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "my.jwt"})
	rec := httptest.NewRecorder()
	s.handleUIProfile(rec, req)

	if gotAuth != "Bearer my.jwt" || gotPath != "/me" {
		t.Fatalf("proxy: auth=%q path=%q", gotAuth, gotPath)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "me@example.com") || !strings.Contains(body, "11111111-1111-1111-1111-111111111111") {
		t.Error("profile page must show the email and id")
	}
}

func TestProfileRedirectsWithoutCookie(t *testing.T) {
	s := newLoginServer(httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("must not call userservice without a session")
	})))
	rec := httptest.NewRecorder()
	s.handleUIProfile(rec, newReq(http.MethodGet, "/ui/profile", nil))
	if rec.Code != http.StatusSeeOther {
		t.Errorf("no cookie: got %d, want 303 redirect", rec.Code)
	}
}
