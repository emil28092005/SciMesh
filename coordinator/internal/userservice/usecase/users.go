package usecase

import (
	"context"

	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/domain"
)

// ListUsers returns every account for the coordinator admin console. The
// handler must project the entities so password hashes never leave the service.
type ListUsers struct {
	users UserRepository
}

func NewListUsers(users UserRepository) *ListUsers {
	return &ListUsers{users: users}
}

func (uc *ListUsers) Execute(ctx context.Context) ([]*domain.User, error) {
	return uc.users.ListUsers(ctx)
}
