// Package authctx carries the authenticated requester across the transport and
// use-case layers without either one importing the other. The HTTP middleware
// stamps a Requester after verifying a user's JWT; the job use cases read it to
// record ownership and to enforce that a non-admin only touches their own jobs.
package authctx

import (
	"context"

	"github.com/google/uuid"
)

// Requester is the identity behind a request, derived from a verified JWT.
// A request authenticated only by the shared worker/service token carries no
// Requester at all (From returns ok=false), which is how worker traffic and
// legacy unauthenticated-user traffic stay owner-less.
type Requester struct {
	UserID   uuid.UUID
	Role     string
	Verified bool
}

// IsAdmin reports whether the requester may act on any user's jobs.
func (r Requester) IsAdmin() bool { return r.Role == "admin" }

// IsTrusted reports whether workers this requester registers produce results
// the coordinator accepts without quorum. Admins and verified contributors are
// trusted; a plain unverified user is not.
func (r Requester) IsTrusted() bool { return r.IsAdmin() || r.Verified }

type ctxKey struct{}

// With returns a copy of ctx carrying r.
func With(ctx context.Context, r Requester) context.Context {
	return context.WithValue(ctx, ctxKey{}, r)
}

// From returns the requester stamped by the middleware, or ok=false when the
// request was not authenticated as a user.
func From(ctx context.Context) (Requester, bool) {
	r, ok := ctx.Value(ctxKey{}).(Requester)
	return r, ok
}
