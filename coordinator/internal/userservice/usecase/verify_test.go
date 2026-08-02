package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/memstore"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/usecase"
)

func TestSetVerifiedGrantsAndRevokes(t *testing.T) {
	reg, _, users := newFixtures()
	ctx := context.Background()

	u, err := reg.Execute(ctx, "contrib@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if u.Verified {
		t.Fatal("a fresh account must be unverified")
	}

	sv := usecase.NewSetVerified(users)

	if err := sv.Execute(ctx, u.ID, true); err != nil {
		t.Fatalf("grant: %v", err)
	}
	got, _ := users.GetByID(ctx, u.ID)
	if !got.Verified {
		t.Error("verified flag not set")
	}

	if err := sv.Execute(ctx, u.ID, false); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got, _ = users.GetByID(ctx, u.ID)
	if got.Verified {
		t.Error("verified flag not cleared")
	}
}

func TestSetVerifiedUnknownUser(t *testing.T) {
	users := memstore.NewUserRepo()
	sv := usecase.NewSetVerified(users)

	if err := sv.Execute(context.Background(), uuid.New(), true); !errors.Is(err, usecase.ErrUserNotFound) {
		t.Errorf("got %v, want ErrUserNotFound", err)
	}
}

func TestLoginTokenCarriesVerified(t *testing.T) {
	reg, login, users := newFixtures()
	ctx := context.Background()

	u, err := reg.Execute(ctx, "trusted@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if err := usecase.NewSetVerified(users).Execute(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}

	_, loggedIn, err := login.Execute(ctx, "trusted@example.com", "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !loggedIn.Verified {
		t.Error("login must reflect the granted verified flag")
	}
}
