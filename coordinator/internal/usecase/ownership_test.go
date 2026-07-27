package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/authctx"
	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

func TestOwnerFromContext(t *testing.T) {
	if ownerFromContext(context.Background()) != nil {
		t.Error("no requester must yield a nil owner")
	}
	id := uuid.New()
	ctx := authctx.With(context.Background(), authctx.Requester{UserID: id, Role: "user"})
	got := ownerFromContext(ctx)
	if got == nil || *got != id {
		t.Errorf("owner = %v, want %v", got, id)
	}
}

func TestAuthorizeJobAccess(t *testing.T) {
	owner := uuid.New()
	other := uuid.New()
	job := &domain.Job{ID: uuid.New(), OwnerID: &owner}

	ctxOf := func(id uuid.UUID, role string) context.Context {
		return authctx.With(context.Background(), authctx.Requester{UserID: id, Role: role})
	}

	cases := []struct {
		name    string
		ctx     context.Context
		wantErr bool
	}{
		{"no requester (worker/legacy) allowed", context.Background(), false},
		{"owner allowed", ctxOf(owner, "user"), false},
		{"admin allowed", ctxOf(other, "admin"), false},
		{"non-owner denied", ctxOf(other, "user"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := authorizeJobAccess(tc.ctx, job)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrJobNotFound) {
					t.Errorf("got %v, want ErrJobNotFound", err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestAuthorizeJobAccessNilOwner(t *testing.T) {
	// A legacy job with no owner must not be readable by an arbitrary user.
	job := &domain.Job{ID: uuid.New(), OwnerID: nil}
	ctx := authctx.With(context.Background(), authctx.Requester{UserID: uuid.New(), Role: "user"})
	if err := authorizeJobAccess(ctx, job); !errors.Is(err, domain.ErrJobNotFound) {
		t.Errorf("got %v, want ErrJobNotFound", err)
	}
}
