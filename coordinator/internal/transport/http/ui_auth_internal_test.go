package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/authctx"
	tokenpkg "github.com/emil28092005/SciMesh/coordinator/internal/token"
)

// newReq builds a request carrying a context, which http.NewRequestWithContext
// provides on go1.22 (httptest.NewRequestWithContext needs go1.23).
func newReq(method, target string, body io.Reader) *http.Request {
	req, err := http.NewRequestWithContext(context.Background(), method, target, body)
	if err != nil {
		panic(err)
	}
	return req
}

type stubVerifier struct {
	claims tokenpkg.Claims
	err    error
}

func (s stubVerifier) Verify(string) (tokenpkg.Claims, error) { return s.claims, s.err }

func TestWithUISessionRedirectsWithoutCookie(t *testing.T) {
	h := withUISession(stubVerifier{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run without a session")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodGet, "/ui", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/login" {
		t.Errorf("redirect = %q, want /ui/login", loc)
	}
}

func TestWithUISessionAcceptsValidCookieAndStampsRequester(t *testing.T) {
	id := uuid.New()
	verifier := stubVerifier{claims: tokenpkg.Claims{UserID: id, Role: "admin", Verified: true}}

	var gotReq authctx.Requester
	var ok bool
	h := withUISession(verifier)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotReq, ok = authctx.From(r.Context())
	}))

	req := newReq(http.MethodGet, "/ui", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "valid.jwt"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !ok || gotReq.UserID != id || gotReq.Role != "admin" || !gotReq.Verified {
		t.Errorf("requester = %+v (ok=%v), want id=%v admin verified", gotReq, ok, id)
	}
}

func TestWithUISessionClearsInvalidCookie(t *testing.T) {
	h := withUISession(stubVerifier{err: errors.New("expired")})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run with an invalid token")
	}))
	req := newReq(http.MethodGet, "/ui", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "stale.jwt"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want 303", rec.Code)
	}
	if c := rec.Result().Cookies(); len(c) == 0 || c[0].MaxAge >= 0 {
		t.Error("stale cookie must be cleared (MaxAge < 0)")
	}
}

// newLoginServer builds a Server whose userservice calls hit stub.
func newLoginServer(stub *httptest.Server) *Server {
	return &Server{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		userserviceURL: strings.TrimRight(stub.URL, "/"),
		httpClient:     stub.Client(),
	}
}

func postForm(path string, form url.Values) *http.Request {
	req := newReq(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestHandleUILoginSetsCookieOnSuccess(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"token":"issued.jwt.here"}`))
	}))
	defer stub.Close()
	s := newLoginServer(stub)

	rec := httptest.NewRecorder()
	s.handleUILogin(rec, postForm("/ui/login", url.Values{"email": {"a@b.com"}, "password": {"password123"}}))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/ui/admin" {
		t.Fatalf("got %d -> %q, want 303 -> /ui/admin", rec.Code, rec.Header().Get("Location"))
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != sessionCookie || cookies[0].Value != "issued.jwt.here" {
		t.Errorf("session cookie not set: %+v", cookies)
	}
	if !cookies[0].HttpOnly {
		t.Error("session cookie must be httpOnly")
	}
}

func TestHandleUILoginRejectsBadCredentials(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer stub.Close()
	s := newLoginServer(stub)

	rec := httptest.NewRecorder()
	s.handleUILogin(rec, postForm("/ui/login", url.Values{"email": {"a@b.com"}, "password": {"wrong"}}))

	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/ui/login?error=") {
		t.Fatalf("got %d -> %q, want 303 -> /ui/login?error=", rec.Code, rec.Header().Get("Location"))
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("no cookie must be set on failed login")
	}
}

func TestHandleUIRegisterConflict(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer stub.Close()
	s := newLoginServer(stub)

	rec := httptest.NewRecorder()
	s.handleUIRegister(rec, postForm("/ui/register", url.Values{"email": {"dup@b.com"}, "password": {"password123"}}))

	if got := rec.Header().Get("Location"); !strings.Contains(got, "already+registered") {
		t.Errorf("register conflict redirect = %q", got)
	}
}

func TestHandleUILogoutClearsCookie(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := httptest.NewRecorder()
	s.handleUILogout(rec, newReq(http.MethodPost, "/ui/logout", nil))

	if rec.Header().Get("Location") != "/ui/login" {
		t.Errorf("logout redirect = %q", rec.Header().Get("Location"))
	}
	c := rec.Result().Cookies()
	if len(c) == 0 || c[0].MaxAge >= 0 {
		t.Error("logout must clear the session cookie")
	}
}

func TestHandleUILoginRedirectsToNext(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"token":"t"}`))
	}))
	defer stub.Close()
	s := newLoginServer(stub)

	// A UI-scoped next is honoured: the admin lands back on the console.
	rec := httptest.NewRecorder()
	s.handleUILogin(rec, postForm("/ui/login", url.Values{"email": {"a@b.com"}, "password": {"p"}, "next": {"/ui/admin"}}))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/ui/admin" {
		t.Errorf("got %d -> %q, want 303 -> /ui/admin", rec.Code, rec.Header().Get("Location"))
	}

	// Anything outside the UI prefix must not become a redirect target.
	for _, next := range []string{"https://evil.example", "/", "//evil.example", "/api/jobs"} {
		rec = httptest.NewRecorder()
		s.handleUILogin(rec, postForm("/ui/login", url.Values{"email": {"a@b.com"}, "password": {"p"}, "next": {next}}))
		if loc := rec.Header().Get("Location"); loc != "/ui/admin" {
			t.Errorf("next=%q landed on %q, want /ui/admin (no open redirect)", next, loc)
		}
	}
}

func TestRedirectToLoginCarriesNext(t *testing.T) {
	rec := httptest.NewRecorder()
	req := newReq(http.MethodGet, "/ui/admin", nil)
	redirectToLogin(rec, req)
	if loc := rec.Header().Get("Location"); loc != "/ui/login?next=%2Fui%2Fadmin" {
		t.Errorf("location = %q, want /ui/login?next=%%2Fui%%2Fadmin", loc)
	}

	// Paths outside the UI stay on the plain login.
	rec = httptest.NewRecorder()
	req = newReq(http.MethodGet, "/health", nil)
	redirectToLogin(rec, req)
	if loc := rec.Header().Get("Location"); loc != "/ui/login" {
		t.Errorf("location = %q, want /ui/login", loc)
	}
}

func TestLoginFormRendersNext(t *testing.T) {
	html := render(t, "login.html", map[string]any{"Next": "/ui/admin"})
	if !strings.Contains(html, `name="next" value="/ui/admin"`) {
		t.Error("login form must carry the next field")
	}
	html = render(t, "login.html", map[string]any{})
	if strings.Contains(html, `name="next"`) {
		t.Error("login form must not render next when absent")
	}
}
