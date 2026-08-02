// Package usecase holds the application logic — registration and login — plus
// the ports (interfaces) it depends on. The concrete adapters (PostgreSQL,
// bcrypt, JWT) are injected from cmd, so this package never imports them.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/domain"
)

// UserRepository persists and looks up users. Implementations return the
// sentinel errors in errors.go so the use cases can react without knowing about
// SQL or driver types.
type UserRepository interface {
	// Insert stores a new user, returning ErrEmailExists if the email is taken.
	Insert(ctx context.Context, u *domain.User) error
	// GetByEmail returns the user with the (normalised) email, or ErrUserNotFound.
	GetByEmail(ctx context.Context, email string) (*domain.User, error)
	// GetByID returns the user with id, or ErrUserNotFound.
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	// SetVerified toggles the verified flag, returning ErrUserNotFound if no
	// such user exists.
	SetVerified(ctx context.Context, id uuid.UUID, verified bool) error
	// SetRole changes a user's role, returning ErrUserNotFound if no such user
	// exists.
	SetRole(ctx context.Context, id uuid.UUID, role domain.Role) error
}

// WorkerKeyRepository persists and looks up the long-lived worker keys a user
// creates to run a worker bound to their account. Implementations return the
// sentinel errors in errors.go so the use cases stay free of SQL types.
type WorkerKeyRepository interface {
	// Insert stores a freshly minted key.
	Insert(ctx context.Context, k *domain.WorkerKey) error
	// ListByUser returns a user's live (non-revoked) keys, newest first.
	ListByUser(ctx context.Context, userID uuid.UUID) ([]*domain.WorkerKey, error)
	// GetActiveByHash returns the non-revoked key with the given hash, or
	// ErrWorkerKeyNotFound.
	GetActiveByHash(ctx context.Context, tokenHash string) (*domain.WorkerKey, error)
	// Revoke retires a key the user owns, returning ErrWorkerKeyNotFound when no
	// live key with that id belongs to the user.
	Revoke(ctx context.Context, id, userID uuid.UUID) error
	// TouchLastUsed records a successful exchange. Best-effort: a failure here
	// must not fail the exchange itself.
	TouchLastUsed(ctx context.Context, id uuid.UUID) error
}

// PasswordHasher hashes and verifies passwords. The bcrypt adapter satisfies it.
type PasswordHasher interface {
	Hash(password string) (string, error)
	Compare(hash, password string) error
}

// TokenIssuer mints a signed access token for an authenticated user. It takes
// the whole user so trust-bearing claims (role, verified) travel in the token.
type TokenIssuer interface {
	Issue(u *domain.User) (string, error)
}

// Clock reads the current time; a fake one makes tests deterministic.
type Clock interface {
	Now() time.Time
}
