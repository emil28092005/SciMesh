package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/usecase"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(context.Background(), db, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(context.Background(), db, nil); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestUserRepoRoundTripAndSentinels(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := NewUserRepo(db)

	user, err := domain.NewUser("root@scimesh.local", "hash", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Insert(ctx, user); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByEmail(ctx, "root@scimesh.local")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != user.ID || got.Role != domain.RoleUser || got.Verified {
		t.Errorf("user = %+v", got)
	}
	duplicate, err := domain.NewUser("ROOT@scimesh.local", "hash2", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Insert(ctx, duplicate); !errors.Is(err, usecase.ErrEmailExists) {
		t.Errorf("duplicate email err = %v, want ErrEmailExists", err)
	}
	if _, err := repo.GetByID(ctx, uuid.New()); !errors.Is(err, usecase.ErrUserNotFound) {
		t.Errorf("missing user err = %v, want ErrUserNotFound", err)
	}
	if err := repo.SetVerified(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Verified {
		t.Error("verified flag did not persist")
	}
	if err := repo.SetRole(ctx, user.ID, domain.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != domain.RoleAdmin {
		t.Errorf("role = %q, want admin", got.Role)
	}
}

func TestWorkerKeyRepoLifecycle(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := NewWorkerKeyRepo(db)

	user, err := domain.NewUser("worker@scimesh.local", "hash", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := NewUserRepo(db).Insert(ctx, user); err != nil {
		t.Fatal(err)
	}

	key, plaintext, err := domain.NewWorkerKey(user.ID, "my machine", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if plaintext == "" || key.TokenHash == "" {
		t.Fatal("key must carry a hash and return a plaintext")
	}
	if err := repo.Insert(ctx, key); err != nil {
		t.Fatal(err)
	}
	found, err := repo.GetActiveByHash(ctx, key.TokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != key.ID {
		t.Errorf("key = %+v", found)
	}
	keys, err := repo.ListByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Errorf("keys = %d, want 1", len(keys))
	}
	if err := repo.TouchLastUsed(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Revoke(ctx, key.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetActiveByHash(ctx, key.TokenHash); !errors.Is(err, usecase.ErrWorkerKeyNotFound) {
		t.Errorf("revoked key err = %v, want ErrWorkerKeyNotFound", err)
	}
	if err := repo.Revoke(ctx, key.ID, user.ID); !errors.Is(err, usecase.ErrWorkerKeyNotFound) {
		t.Errorf("double revoke err = %v, want ErrWorkerKeyNotFound", err)
	}
}
