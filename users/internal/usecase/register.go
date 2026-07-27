package usecase

import (
	"context"

	"github.com/emil28092005/SciMesh/users/internal/domain"
)

const (
	// minPasswordLen is a floor, not a policy engine — enough to reject the
	// obviously weak without pretending to measure real strength.
	minPasswordLen = 8
	// maxPasswordLen is bcrypt's hard input limit: it ignores bytes past 72, so
	// accepting a longer password would silently hash only its prefix.
	maxPasswordLen = 72
)

// Register creates a new account: it validates the password, hashes it, builds
// the domain user, and persists it.
type Register struct {
	users  UserRepository
	hasher PasswordHasher
	clk    Clock
}

func NewRegister(users UserRepository, hasher PasswordHasher, clk Clock) *Register {
	return &Register{users: users, hasher: hasher, clk: clk}
}

// Execute registers email/password and returns the persisted user. The returned
// user carries no plaintext password, only its hash.
func (r *Register) Execute(ctx context.Context, email, password string) (*domain.User, error) {
	if len(password) < minPasswordLen {
		return nil, ErrPasswordTooShort
	}
	if len(password) > maxPasswordLen {
		return nil, ErrPasswordTooLong
	}

	hash, err := r.hasher.Hash(password)
	if err != nil {
		return nil, err
	}

	// NewUser normalises the email and enforces its shape; it returns a domain
	// validation error the transport layer maps to 400.
	u, err := domain.NewUser(email, hash, r.clk.Now())
	if err != nil {
		return nil, err
	}

	if err := r.users.Insert(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}
