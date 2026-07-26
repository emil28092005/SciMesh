package usecase

import (
	"context"
	"errors"

	"github.com/emil28092005/SciMesh/users/internal/domain"
)

// BootstrapAdmin seeds the first admin account. It exists because there is no
// other way to create one: /register always makes a plain user, and promoting a
// user to admin requires an already-existing admin. Running it at startup with
// operator-supplied credentials breaks that chicken-and-egg.
type BootstrapAdmin struct {
	users  UserRepository
	hasher PasswordHasher
	clk    Clock
}

func NewBootstrapAdmin(users UserRepository, hasher PasswordHasher, clk Clock) *BootstrapAdmin {
	return &BootstrapAdmin{users: users, hasher: hasher, clk: clk}
}

// Execute creates the admin if it does not already exist, reporting whether it
// created one. It is idempotent: a second run (a restart) finds the account and
// does nothing, so it is safe to call on every boot.
func (uc *BootstrapAdmin) Execute(ctx context.Context, email, password string) (created bool, err error) {
	email = domain.NormalizeEmail(email)

	if _, err := uc.users.GetByEmail(ctx, email); err == nil {
		return false, nil // already bootstrapped
	} else if !errors.Is(err, ErrUserNotFound) {
		return false, err
	}

	if len(password) < minPasswordLen {
		return false, ErrPasswordTooShort
	}
	if len(password) > maxPasswordLen {
		return false, ErrPasswordTooLong
	}

	hash, err := uc.hasher.Hash(password)
	if err != nil {
		return false, err
	}
	u, err := domain.NewUser(email, hash, uc.clk.Now())
	if err != nil {
		return false, err
	}
	// Direct role assignment is safe here: this is a trusted server-side seed,
	// not a request. A root admin is also a trusted contributor.
	u.Role = domain.RoleAdmin
	u.Verified = true

	if err := uc.users.Insert(ctx, u); err != nil {
		// A concurrent bootstrap (two replicas booting at once) is fine: whoever
		// lost the race just observes the account now exists.
		if errors.Is(err, ErrEmailExists) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
