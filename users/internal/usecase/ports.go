// Package usecase holds the application logic — registration and login — plus
// the ports (interfaces) it depends on. The concrete adapters (PostgreSQL,
// bcrypt, JWT) are injected from cmd, so this package never imports them.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/users/internal/domain"
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
