package usecase

import (
	"context"

	"github.com/google/uuid"
)

// SetVerified grants or revokes a user's trusted-contributor badge. Only an
// admin may call this (enforced in the transport layer); the use case itself
// just applies the change.
type SetVerified struct {
	users UserRepository
}

func NewSetVerified(users UserRepository) *SetVerified {
	return &SetVerified{users: users}
}

// Execute sets the verified flag on the target user, returning ErrUserNotFound
// if the user does not exist.
func (uc *SetVerified) Execute(ctx context.Context, id uuid.UUID, verified bool) error {
	return uc.users.SetVerified(ctx, id, verified)
}
