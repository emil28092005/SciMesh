package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/authctx"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

func adminReq(t *testing.T, role string) *http.Request {
	t.Helper()
	req := newReq(http.MethodGet, "/ui/admin", nil)
	return req.WithContext(authctx.With(context.Background(), authctx.Requester{UserID: uuid.New(), Role: role}))
}

func TestRequireAdminAllowsAdminOnly(t *testing.T) {
	reached := false
	h := requireAdmin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	// Admin passes through.
	h.ServeHTTP(httptest.NewRecorder(), adminReq(t, "admin"))
	if !reached {
		t.Error("admin must reach the handler")
	}

	// Plain user is redirected to the dashboard.
	reached = false
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq(t, "user"))
	if reached {
		t.Error("non-admin must not reach the handler")
	}
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/ui" {
		t.Errorf("non-admin got %d -> %q, want 303 -> /ui", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAdminUserActionForwardsBearer(t *testing.T) {
	targetID := uuid.NewString()
	var gotAuth, gotPath string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer stub.Close()
	s := newLoginServer(stub)

	req := newReq(http.MethodPost, "/ui/admin/user-action",
		strings.NewReader(url.Values{"user_id": {targetID}, "action": {"promote"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "admin.jwt.token"})
	rec := httptest.NewRecorder()
	s.handleUIAdminUserAction(rec, req)

	if gotAuth != "Bearer admin.jwt.token" {
		t.Errorf("forwarded auth = %q, want the admin bearer", gotAuth)
	}
	if gotPath != "/users/"+targetID+"/promote" {
		t.Errorf("forwarded path = %q", gotPath)
	}
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "msg=") {
		t.Errorf("got %d -> %q, want 303 with a success msg", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAdminUserActionRejectsUnknownAction(t *testing.T) {
	s := newLoginServer(httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("userservice must not be called for an invalid action")
	})))
	req := newReq(http.MethodPost, "/ui/admin/user-action",
		strings.NewReader(url.Values{"user_id": {uuid.NewString()}, "action": {"delete"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "x"})
	rec := httptest.NewRecorder()
	s.handleUIAdminUserAction(rec, req)
	if !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Errorf("unknown action redirect = %q, want an error", rec.Header().Get("Location"))
	}
}

func TestAdminUserActionRejectsBadID(t *testing.T) {
	s := newLoginServer(httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("userservice must not be called for an invalid id")
	})))
	req := newReq(http.MethodPost, "/ui/admin/user-action",
		strings.NewReader(url.Values{"user_id": {"not-a-uuid"}, "action": {"promote"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "x"})
	rec := httptest.NewRecorder()
	s.handleUIAdminUserAction(rec, req)
	if !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Errorf("bad id redirect = %q, want an error", rec.Header().Get("Location"))
	}
}

func TestDashboardAdminLinkOnlyForAdmin(t *testing.T) {
	admin := render(t, "dashboard.html", usecase.DashboardView{Session: &usecase.SessionView{Role: "admin"}})
	if !strings.Contains(admin, "/ui/admin") {
		t.Error("admin must see the Admin link")
	}
	user := render(t, "dashboard.html", usecase.DashboardView{Session: &usecase.SessionView{Role: "user"}})
	if strings.Contains(user, "/ui/admin") {
		t.Error("a plain user must not see the Admin link")
	}
}
