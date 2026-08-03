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

	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/auth"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/memstore"
	apihttp "github.com/emil28092005/SciMesh/coordinator/internal/userservice/transport/http"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/usecase"
)

const secret = "server-test-secret-32-bytes-long!!!!"

func newTestServer() http.Handler {
	users := memstore.NewUserRepo()
	keys := memstore.NewWorkerKeyRepo()
	hasher := auth.NewHasher(4)
	clk := memstore.Clock{T: time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)}
	// Real clock for the issuer so tokens are valid at verification time.
	issuer := auth.NewIssuer(secret, time.Hour, nil)

	uc := apihttp.UseCases{
		Register:             usecase.NewRegister(users, hasher, clk),
		Login:                usecase.NewLogin(users, hasher, issuer),
		SetVerified:          usecase.NewSetVerified(users),
		SetRole:              usecase.NewSetRole(users),
		CreateWorkerKey:      usecase.NewCreateWorkerKey(keys, clk),
		ListWorkerKeys:       usecase.NewListWorkerKeys(keys),
		ListWorkerKeysAll:    usecase.NewListWorkerKeysAll(keys),
		RevokeWorkerKey:      usecase.NewRevokeWorkerKey(keys),
		RevokeWorkerKeyAdmin: usecase.NewRevokeWorkerKeyAdmin(keys),
		ExchangeWorkerKey:    usecase.NewExchangeWorkerKey(keys, users, issuer, time.Hour),
		ListUsers:            usecase.NewListUsers(users),
		Users:                users,
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
// given role — enough to drive the admin-gated endpoints. userID defaults to a
// fresh random id; pass one to act as an existing account.
func mintToken(t *testing.T, role domain.Role) string {
	t.Helper()
	return mintTokenFor(t, role, uuid.New())
}

func mintTokenFor(t *testing.T, role domain.Role, userID uuid.UUID) string {
	t.Helper()
	token, err := auth.NewIssuer(secret, time.Hour, nil).Issue(&domain.User{ID: userID, Role: role})
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

func TestAdminListsUsersAndKeys(t *testing.T) {
	h := newTestServer()
	userID, _ := uuid.Parse(registerUser(t, h, "listed@example.com"))
	userToken := mintTokenFor(t, domain.RoleUser, userID)
	// Mint a worker key as the plain user.
	keyRec := do(t, h, http.MethodPost, "/worker-keys", userToken, map[string]string{"name": "lab-node"})
	if keyRec.Code != http.StatusCreated {
		t.Fatalf("create key: got %d, body %s", keyRec.Code, keyRec.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(keyRec.Body.Bytes(), &created)

	// Admin lists users: emails present, password hashes absent.
	rec := do(t, h, http.MethodGet, "/users", mintToken(t, domain.RoleAdmin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list users: got %d, body %s", rec.Code, rec.Body)
	}
	var users struct {
		Users []struct {
			Email        string `json:"email"`
			Role         string `json:"role"`
			PasswordHash string `json:"password_hash"`
		} `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &users); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range users.Users {
		if u.PasswordHash != "" {
			t.Error("password hash leaked through the admin users list")
		}
		if u.Email == "listed@example.com" {
			found = true
		}
	}
	if !found {
		t.Error("listed user missing from the admin list")
	}

	// Admin lists all keys: the owner is attached, no secret.
	rec = do(t, h, http.MethodGet, "/worker-keys/all", mintToken(t, domain.RoleAdmin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list all keys: got %d", rec.Code)
	}
	var keys struct {
		WorkerKeys []struct {
			ID     string `json:"id"`
			UserID string `json:"user_id"`
			Name   string `json:"name"`
		} `json:"worker_keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys.WorkerKeys) != 1 || keys.WorkerKeys[0].UserID != userID.String() {
		t.Errorf("all keys = %+v, want the one key owned by %s", keys.WorkerKeys, userID)
	}

	// Plain users cannot see either list.
	for _, path := range []string{"/users", "/worker-keys/all"} {
		if rec := do(t, h, http.MethodGet, path, mintToken(t, domain.RoleUser), nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s as user: got %d, want 403", path, rec.Code)
		}
	}

	// An admin revokes a key that belongs to another user; the plain owner of
	// that key could not (it would be a 404, scoped to their own keys).
	// The owner of the key revokes it themselves: 204.
	if rec := do(t, h, http.MethodDelete, "/worker-keys/"+created.ID, userToken, nil); rec.Code != http.StatusNoContent {
		t.Errorf("user revoke own key: got %d, want 204", rec.Code)
	}
	// Another plain user cannot revoke it: scoped to their own keys, so a
	// mismatch reads as 404.
	otherID, _ := uuid.Parse(registerUser(t, h, "other@example.com"))
	if rec := do(t, h, http.MethodDelete, "/worker-keys/"+created.ID, mintTokenFor(t, domain.RoleUser, otherID), nil); rec.Code != http.StatusNotFound {
		t.Errorf("other user revoke: got %d, want 404", rec.Code)
	}
	// An admin revokes a key that belongs to someone else: 204.
	keyRec = do(t, h, http.MethodPost, "/worker-keys", userToken, map[string]string{"name": "lab-node-2"})
	var second struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(keyRec.Body.Bytes(), &second)
	if rec := do(t, h, http.MethodDelete, "/worker-keys/"+second.ID, mintToken(t, domain.RoleAdmin), nil); rec.Code != http.StatusNoContent {
		t.Errorf("admin revoke other's key: got %d, want 204", rec.Code)
	}
	// Admin cannot revoke an unknown key.
	if rec := do(t, h, http.MethodDelete, "/worker-keys/"+uuid.NewString(), mintToken(t, domain.RoleAdmin), nil); rec.Code != http.StatusNotFound {
		t.Errorf("admin revoke unknown key: got %d, want 404", rec.Code)
	}
}

func TestRegistrationDisabledEnv(t *testing.T) {
	t.Setenv("USERSERVICE_DISABLE_REGISTRATION", "1")
	h := newTestServer()
	rec := do(t, h, http.MethodPost, "/register", "", map[string]string{"email": "blocked@x.io", "password": "pw"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("register when disabled: got %d, want 403", rec.Code)
	}
}
