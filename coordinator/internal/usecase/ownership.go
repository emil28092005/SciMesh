package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/authctx"
	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// ownerFromContext returns the authenticated user id to stamp on a new job, or
// nil when the request was not authenticated as a user — worker or legacy
// traffic, or user-JWT auth disabled. A nil owner is stored as NULL.
func ownerFromContext(ctx context.Context) *uuid.UUID {
	if r, ok := authctx.From(ctx); ok {
		id := r.UserID
		return &id
	}
	return nil
}

// authorizeJobAccess enforces that a non-admin user may only act on their own
// job. It returns ErrJobNotFound — not a 403 — on a mismatch, so the response
// never reveals that another user's job exists.
//
// Requests with no authenticated user (worker/legacy traffic, or JWT auth
// disabled) are not restricted here: the shared service token already gated
// them, and worker endpoints legitimately operate across all jobs.
func authorizeJobAccess(ctx context.Context, job *domain.Job) error {
	r, ok := authctx.From(ctx)
	if !ok || r.IsAdmin() {
		return nil
	}
	if job.OwnerID == nil || *job.OwnerID != r.UserID {
		return domain.ErrJobNotFound
	}
	return nil
}
