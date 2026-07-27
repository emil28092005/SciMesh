package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/users/internal/auth"
	"github.com/emil28092005/SciMesh/users/internal/domain"
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
		Register:    usecase.NewRegister(users, hasher, clk),
		Login:       usecase.NewLogin(users, hasher, issuer),
		SetVerified: usecase.NewSetVerified(users),
		SetRole:     usecase.NewSetRole(users),
		Users:       users,
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
	req, err := http.NewRequestWithContext(context.Background(), method, path, &buf)
	if err != nil {
		t.Fatal(err)
	}
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

// failingUsers is a UserRepository whose reads fail with an unexpected (non-
// sentinel) error, so the handler must map it to 500 and not leak internals.
type failingUsers struct{ usecase.UserRepository }

func (failingUsers) GetByID(context.Context, uuid.UUID) (*domain.User, error) {
	return nil, errors.New("db exploded")
}

func TestMeInternalError(t *testing.T) {
	hasher := auth.NewHasher(4)
	clk := memstore.Clock{T: time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)}
	issuer := auth.NewIssuer(secret, time.Hour, nil)

	users := failingUsers{UserRepository: memstore.NewUserRepo()}
	uc := apihttp.UseCases{
		Register:    usecase.NewRegister(users, hasher, clk),
		Login:       usecase.NewLogin(users, hasher, issuer),
		SetVerified: usecase.NewSetVerified(users),
		SetRole:     usecase.NewSetRole(users),
		Users:       users,
	}
	h := apihttp.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), uc, issuer)

	// A structurally valid token for a caller the failing repo can't load.
	token, err := issuer.Issue(&domain.User{ID: uuid.New(), Role: domain.RoleUser})
	if err != nil {
		t.Fatal(err)
	}

	rec := do(t, h, http.MethodGet, "/me", token, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("got %d, want 500", rec.Code)
	}
	// The body must not disclose the underlying error.
	if bytes.Contains(rec.Body.Bytes(), []byte("db exploded")) {
		t.Error("internal error leaked to the client")
	}
}

// mintToken issues a token with the package secret for a synthetic caller of the
// given role — enough to drive the admin-gated endpoints.
func mintToken(t *testing.T, role domain.Role) string {
	t.Helper()
	token, err := auth.NewIssuer(secret, time.Hour, nil).Issue(&domain.User{ID: uuid.New(), Role: role})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// registerUser creates an account and returns its id.
func registerUser(t *testing.T, h http.Handler, email string) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/register", "", map[string]string{"email": email, "password": "password123"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d", rec.Code)
	}
	var reg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reg); err != nil {
		t.Fatal(err)
	}
	return reg.ID
}

func TestAdminVerifiesUserEndToEnd(t *testing.T) {
	h := newTestServer()
	id := registerUser(t, h, "contrib@example.com")

	// Admin grants the badge.
	rec := do(t, h, http.MethodPost, "/users/"+id+"/verify", mintToken(t, domain.RoleAdmin), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("admin verify: got %d, body %s", rec.Code, rec.Body)
	}

	// The change is visible when the contributor logs in.
	rec = do(t, h, http.MethodPost, "/login", "", map[string]string{"email": "contrib@example.com", "password": "password123"})
	var lr struct {
		User struct {
			Verified bool `json:"verified"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
		t.Fatal(err)
	}
	if !lr.User.Verified {
		t.Error("verified badge not reflected after admin granted it")
	}
}

func TestVerifyRequiresAdminRole(t *testing.T) {
	h := newTestServer()
	id := registerUser(t, h, "someone@example.com")

	// A plain user token must not be able to grant the badge.
	rec := do(t, h, http.MethodPost, "/users/"+id+"/verify", mintToken(t, domain.RoleUser), nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("plain user: got %d, want 403", rec.Code)
	}
}

func TestVerifyRequiresAuth(t *testing.T) {
	h := newTestServer()
	rec := do(t, h, http.MethodPost, "/users/"+uuid.NewString()+"/verify", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
}

func TestVerifyInvalidID(t *testing.T) {
	h := newTestServer()
	rec := do(t, h, http.MethodPost, "/users/not-a-uuid/verify", mintToken(t, domain.RoleAdmin), nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: got %d, want 400", rec.Code)
	}
}

func TestVerifyUnknownUser(t *testing.T) {
	h := newTestServer()
	rec := do(t, h, http.MethodPost, "/users/"+uuid.NewString()+"/verify", mintToken(t, domain.RoleAdmin), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown user: got %d, want 404", rec.Code)
	}
}

func TestAdminPromotesAndDemotes(t *testing.T) {
	h := newTestServer()
	id := registerUser(t, h, "promote@example.com")
	admin := mintToken(t, domain.RoleAdmin)

	if rec := do(t, h, http.MethodPost, "/users/"+id+"/promote", admin, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("promote: got %d, body %s", rec.Code, rec.Body)
	}
	// The promoted user now logs in as an admin.
	rec := do(t, h, http.MethodPost, "/login", "", map[string]string{"email": "promote@example.com", "password": "password123"})
	var lr struct {
		User struct {
			Role string `json:"role"`
		} `json:"user"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &lr)
	if lr.User.Role != "admin" {
		t.Errorf("role after promote = %q, want admin", lr.User.Role)
	}

	if rec := do(t, h, http.MethodPost, "/users/"+id+"/demote", admin, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("demote: got %d", rec.Code)
	}
}

func TestPromoteRequiresAdmin(t *testing.T) {
	h := newTestServer()
	id := registerUser(t, h, "target@example.com")

	if rec := do(t, h, http.MethodPost, "/users/"+id+"/promote", mintToken(t, domain.RoleUser), nil); rec.Code != http.StatusForbidden {
		t.Errorf("plain user promote: got %d, want 403", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/users/"+id+"/promote", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
}

func TestPromoteUnknownUser(t *testing.T) {
	h := newTestServer()
	rec := do(t, h, http.MethodPost, "/users/"+uuid.NewString()+"/promote", mintToken(t, domain.RoleAdmin), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown user promote: got %d, want 404", rec.Code)
	}
}

func TestUnverifyRevokes(t *testing.T) {
	h := newTestServer()
	id := registerUser(t, h, "revoke@example.com")
	admin := mintToken(t, domain.RoleAdmin)

	if rec := do(t, h, http.MethodPost, "/users/"+id+"/verify", admin, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("verify: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/users/"+id+"/unverify", admin, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unverify: %d", rec.Code)
	}

	rec := do(t, h, http.MethodPost, "/login", "", map[string]string{"email": "revoke@example.com", "password": "password123"})
	var lr struct {
		User struct {
			Verified bool `json:"verified"`
		} `json:"user"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &lr)
	if lr.User.Verified {
		t.Error("verified should be false after unverify")
	}
}
