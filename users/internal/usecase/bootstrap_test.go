package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/emil28092005/SciMesh/users/internal/auth"
	"github.com/emil28092005/SciMesh/users/internal/domain"
	"github.com/emil28092005/SciMesh/users/internal/memstore"
	"github.com/emil28092005/SciMesh/users/internal/usecase"
)

func newBootstrap() (*usecase.BootstrapAdmin, *memstore.UserRepo) {
	users := memstore.NewUserRepo()
	hasher := auth.NewHasher(4)
	clk := memstore.Clock{T: time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)}
	return usecase.NewBootstrapAdmin(users, hasher, clk), users
}

func TestBootstrapCreatesAdmin(t *testing.T) {
	bs, users := newBootstrap()

	created, err := bs.Execute(context.Background(), "Root@Example.com", "rootpassword")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !created {
		t.Fatal("expected an admin to be created")
	}

	u, err := users.GetByEmail(context.Background(), "root@example.com")
	if err != nil {
		t.Fatalf("admin not persisted: %v", err)
	}
	if u.Role != domain.RoleAdmin {
		t.Errorf("role = %q, want admin", u.Role)
	}
	if !u.Verified {
		t.Error("bootstrap admin should be verified")
	}
}

func TestBootstrapIsIdempotent(t *testing.T) {
	bs, users := newBootstrap()
	ctx := context.Background()

	if _, err := bs.Execute(ctx, "root@example.com", "rootpassword"); err != nil {
		t.Fatal(err)
	}
	created, err := bs.Execute(ctx, "root@example.com", "rootpassword")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if created {
		t.Error("second run must not create a duplicate admin")
	}

	// The account must still be a single admin.
	if u, _ := users.GetByEmail(ctx, "root@example.com"); u.Role != domain.RoleAdmin {
		t.Errorf("role changed: %q", u.Role)
	}
}

func TestBootstrapRejectsWeakPassword(t *testing.T) {
	bs, _ := newBootstrap()
	if _, err := bs.Execute(context.Background(), "root@example.com", "short"); !errors.Is(err, usecase.ErrPasswordTooShort) {
		t.Errorf("got %v, want ErrPasswordTooShort", err)
	}
}
