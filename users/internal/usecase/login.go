package usecase

import (
	"context"
	"errors"

	"github.com/emil28092005/SciMesh/users/internal/domain"
)

// Login verifies credentials and issues a signed token.
type Login struct {
	users  UserRepository
	hasher PasswordHasher
	tokens TokenIssuer
}

func NewLogin(users UserRepository, hasher PasswordHasher, tokens TokenIssuer) *Login {
	return &Login{users: users, hasher: hasher, tokens: tokens}
}

// Execute returns a signed token and the user on success. It returns
// ErrInvalidCredentials for both an unknown email and a wrong password so the
// two cases are indistinguishable to a caller probing for valid accounts.
func (l *Login) Execute(ctx context.Context, email, password string) (string, *domain.User, error) {
	u, err := l.users.GetByEmail(ctx, domain.NormalizeEmail(email))
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return "", nil, ErrInvalidCredentials
		}
		return "", nil, err
	}

	if err := l.hasher.Compare(u.PasswordHash, password); err != nil {
		return "", nil, ErrInvalidCredentials
	}

	token, err := l.tokens.Issue(u)
	if err != nil {
		return "", nil, err
	}
	return token, u, nil
}
