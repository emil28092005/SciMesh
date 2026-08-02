package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/domain"
)

// SetRole promotes or demotes a user. Only an admin may call this (enforced in
// the transport layer); the use case validates the target role and applies it.
type SetRole struct {
	users UserRepository
}

func NewSetRole(users UserRepository) *SetRole {
	return &SetRole{users: users}
}

// Execute assigns role to the user, returning ErrInvalidRole for an unknown role
// or ErrUserNotFound if the user does not exist.
func (uc *SetRole) Execute(ctx context.Context, id uuid.UUID, role domain.Role) error {
	if !role.Valid() {
		return ErrInvalidRole
	}
	return uc.users.SetRole(ctx, id, role)
}
