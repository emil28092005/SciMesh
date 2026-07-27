package usecase_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emil28092005/SciMesh/users/internal/auth"
	"github.com/emil28092005/SciMesh/users/internal/domain"
	"github.com/emil28092005/SciMesh/users/internal/memstore"
	"github.com/emil28092005/SciMesh/users/internal/usecase"
)

const secret = "usecase-test-secret-32-bytes-long!!!"

func newFixtures() (*usecase.Register, *usecase.Login, *memstore.UserRepo) {
	users := memstore.NewUserRepo()
	hasher := auth.NewHasher(4) // low cost keeps tests fast
	clk := memstore.Clock{T: time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)}
	// The issuer uses the real clock (nil): token expiry is validated against
	// wall-clock time, so a fixed issue-time would make tokens instantly stale.
	issuer := auth.NewIssuer(secret, time.Hour, nil)

	reg := usecase.NewRegister(users, hasher, clk)
	login := usecase.NewLogin(users, hasher, issuer)
	return reg, login, users
}

func TestRegisterSuccess(t *testing.T) {
	reg, _, users := newFixtures()

	u, err := reg.Execute(context.Background(), "Alice@Example.com", "password123")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if u.Email != "alice@example.com" {
		t.Errorf("email not normalised: %q", u.Email)
	}
	if u.Role != domain.RoleUser {
		t.Errorf("role = %q, want user", u.Role)
	}
	if strings.Contains(u.PasswordHash, "password123") {
		t.Error("password stored in cleartext")
	}
	if _, err := users.GetByEmail(context.Background(), "alice@example.com"); err != nil {
		t.Errorf("user not persisted: %v", err)
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	reg, _, _ := newFixtures()
	ctx := context.Background()

	if _, err := reg.Execute(ctx, "dup@example.com", "password123"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	_, err := reg.Execute(ctx, "Dup@example.com", "password123") // different case, same email
	if !errors.Is(err, usecase.ErrEmailExists) {
		t.Errorf("got %v, want ErrEmailExists", err)
	}
}

func TestRegisterPasswordPolicy(t *testing.T) {
	reg, _, _ := newFixtures()
	ctx := context.Background()

	if _, err := reg.Execute(ctx, "a@b.com", "short"); !errors.Is(err, usecase.ErrPasswordTooShort) {
		t.Errorf("short password: got %v", err)
	}
	long := strings.Repeat("x", 73)
	if _, err := reg.Execute(ctx, "a@b.com", long); !errors.Is(err, usecase.ErrPasswordTooLong) {
		t.Errorf("long password: got %v", err)
	}
}

func TestRegisterInvalidEmail(t *testing.T) {
	reg, _, _ := newFixtures()
	_, err := reg.Execute(context.Background(), "not-an-email", "password123")
	if !errors.Is(err, domain.ErrInvalidEmail) {
		t.Errorf("got %v, want ErrInvalidEmail", err)
	}
}

func TestLoginSuccess(t *testing.T) {
	reg, login, _ := newFixtures()
	ctx := context.Background()
	if _, err := reg.Execute(ctx, "user@example.com", "password123"); err != nil {
		t.Fatal(err)
	}

	token, u, err := login.Execute(ctx, "User@Example.com", "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if token == "" {
		t.Error("empty token")
	}
	if u.Email != "user@example.com" {
		t.Errorf("wrong user returned: %q", u.Email)
	}

	// The token must verify and carry this user's id.
	claims, err := auth.NewIssuer(secret, time.Hour, nil).Verify(token)
	if err != nil {
		t.Fatalf("issued token does not verify: %v", err)
	}
	if claims.Subject != u.ID.String() {
		t.Errorf("token sub = %q, want %q", claims.Subject, u.ID.String())
	}
}

func TestLoginWrongPassword(t *testing.T) {
	reg, login, _ := newFixtures()
	ctx := context.Background()
	if _, err := reg.Execute(ctx, "user@example.com", "password123"); err != nil {
		t.Fatal(err)
	}

	_, _, err := login.Execute(ctx, "user@example.com", "wrongpass1")
	if !errors.Is(err, usecase.ErrInvalidCredentials) {
		t.Errorf("got %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginUnknownEmailIsIndistinguishable(t *testing.T) {
	_, login, _ := newFixtures()
	_, _, err := login.Execute(context.Background(), "ghost@example.com", "password123")
	if !errors.Is(err, usecase.ErrInvalidCredentials) {
		t.Errorf("unknown email must return ErrInvalidCredentials, got %v", err)
	}
}
