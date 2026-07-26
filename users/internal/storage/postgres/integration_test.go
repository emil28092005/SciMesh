//go:build integration

// Integration tests run against a real PostgreSQL instance supplied through
// TEST_DATABASE_URL, with the userservice migrations already applied. A real DB
// is required because the guarantees under test — the unique-email constraint
// mapping to ErrEmailExists, the ck_users_email_lower check — are properties of
// Postgres, not of the Go code.
//
//	docker compose up -d
//	TEST_DATABASE_URL='postgres://scimesh:scimesh@localhost:5432/scimesh?sslmode=disable' \
//	  go test -tags=integration ./internal/storage/postgres/ -v
package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emil28092005/SciMesh/users/internal/domain"
	"github.com/emil28092005/SciMesh/users/internal/usecase"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedUser inserts a user with a unique email and removes it afterwards, so
// tests stay independent of each other and of leftovers from earlier runs.
func seedUser(t *testing.T, repo *UserRepo) *domain.User {
	t.Helper()
	email := fmt.Sprintf("it-%s@example.com", uuid.NewString())
	u, err := domain.NewUser(email, "$2a$04$abcdefghijklmnopqrstuv", time.Now().UTC())
	if err != nil {
		t.Fatalf("build user: %v", err)
	}
	if err := repo.Insert(context.Background(), u); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", u.ID)
	})
	return u
}

func TestUserRepoInsertAndGet(t *testing.T) {
	repo := NewUserRepo(testPool(t))
	ctx := context.Background()
	want := seedUser(t, repo)

	byEmail, err := repo.GetByEmail(ctx, want.Email)
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if byEmail.ID != want.ID || byEmail.Email != want.Email || byEmail.Role != domain.RoleUser {
		t.Errorf("GetByEmail mismatch: %+v", byEmail)
	}

	byID, err := repo.GetByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if byID.Email != want.Email {
		t.Errorf("GetByID mismatch: %+v", byID)
	}
}

func TestUserRepoDuplicateEmail(t *testing.T) {
	repo := NewUserRepo(testPool(t))
	existing := seedUser(t, repo)

	// A second user with the same email must hit the unique constraint and map
	// to the port's sentinel error.
	dup, err := domain.NewUser(existing.Email, "$2a$04$abcdefghijklmnopqrstuv", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	err = repo.Insert(context.Background(), dup)
	if !errors.Is(err, usecase.ErrEmailExists) {
		t.Errorf("got %v, want ErrEmailExists", err)
	}
}

func TestUserRepoNotFound(t *testing.T) {
	repo := NewUserRepo(testPool(t))
	ctx := context.Background()

	if _, err := repo.GetByID(ctx, uuid.New()); !errors.Is(err, usecase.ErrUserNotFound) {
		t.Errorf("GetByID unknown: got %v, want ErrUserNotFound", err)
	}
	if _, err := repo.GetByEmail(ctx, "ghost@example.com"); !errors.Is(err, usecase.ErrUserNotFound) {
		t.Errorf("GetByEmail unknown: got %v, want ErrUserNotFound", err)
	}
}
