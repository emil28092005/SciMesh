package http_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/emil28092005/SciMesh/users/internal/auth"
	"github.com/emil28092005/SciMesh/users/internal/memstore"
	apihttp "github.com/emil28092005/SciMesh/users/internal/transport/http"
	"github.com/emil28092005/SciMesh/users/internal/usecase"
)

const secret = "server-test-secret-32-bytes-long!!!!"

func newTestServer() http.Handler {
	users := memstore.NewUserRepo()
	hasher := auth.NewHasher(4)
	clk := memstore.Clock{T: time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)}
	// Real clock for the issuer so tokens are valid at verification time.
	issuer := auth.NewIssuer(secret, time.Hour, nil)

	uc := apihttp.UseCases{
		Register: usecase.NewRegister(users, hasher, clk),
		Login:    usecase.NewLogin(users, hasher, issuer),
		Users:    users,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return apihttp.NewServer(log, uc, issuer)
}

func do(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRegisterThenLoginThenMe(t *testing.T) {
	h := newTestServer()
	creds := map[string]string{"email": "flow@example.com", "password": "password123"}

	// Register -> 201
	rec := do(t, h, http.MethodPost, "/register", "", creds)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: got %d, body %s", rec.Code, rec.Body)
	}

	// Login -> 200 with a token
	rec = do(t, h, http.MethodPost, "/login", "", creds)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: got %d, body %s", rec.Code, rec.Body)
	}
	var lr struct {
		Token string `json:"token"`
		User  struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
		t.Fatal(err)
	}
	if lr.Token == "" || lr.User.Email != "flow@example.com" || lr.User.Role != "user" {
		t.Fatalf("unexpected login body: %+v", lr)
	}

	// /me with the token -> 200, same user
	rec = do(t, h, http.MethodGet, "/me", lr.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("me: got %d, body %s", rec.Code, rec.Body)
	}
	var me struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.Email != "flow@example.com" {
		t.Errorf("me email = %q", me.Email)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	h := newTestServer()
	creds := map[string]string{"email": "dup@example.com", "password": "password123"}
	_ = do(t, h, http.MethodPost, "/register", "", creds)

	rec := do(t, h, http.MethodPost, "/register", "", creds)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate register: got %d, want 409", rec.Code)
	}
}

func TestRegisterValidation(t *testing.T) {
	h := newTestServer()
	cases := []struct {
		name string
		body map[string]string
	}{
		{"weak password", map[string]string{"email": "a@b.com", "password": "short"}},
		{"bad email", map[string]string{"email": "nope", "password": "password123"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, "/register", "", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("got %d, want 400", rec.Code)
			}
		})
	}
}

func TestRegisterRejectsUnknownFields(t *testing.T) {
	h := newTestServer()
	rec := do(t, h, http.MethodPost, "/register", "", map[string]string{
		"email": "a@b.com", "password": "password123", "role": "admin",
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field must be rejected: got %d", rec.Code)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	h := newTestServer()
	_ = do(t, h, http.MethodPost, "/register", "", map[string]string{
		"email": "x@example.com", "password": "password123",
	})
	rec := do(t, h, http.MethodPost, "/login", "", map[string]string{
		"email": "x@example.com", "password": "wrongpass1",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}

func TestMeRequiresToken(t *testing.T) {
	h := newTestServer()
	if rec := do(t, h, http.MethodGet, "/me", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/me", "garbage.token.here", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token: got %d, want 401", rec.Code)
	}
}

func TestHealth(t *testing.T) {
	h := newTestServer()
	if rec := do(t, h, http.MethodGet, "/health", "", nil); rec.Code != http.StatusOK {
		t.Errorf("health: got %d", rec.Code)
	}
}
