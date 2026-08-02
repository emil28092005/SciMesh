// Package memstore provides in-memory implementations of the usecase ports for
// fast, deterministic tests that need no database.
package memstore

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/usecase"
)

// UserRepo is an in-memory usecase.UserRepository. It stores copies, so callers
// mutating a returned user cannot corrupt the store.
type UserRepo struct {
	mu      sync.Mutex
	byID    map[uuid.UUID]domain.User
	byEmail map[string]uuid.UUID
}

func NewUserRepo() *UserRepo {
	return &UserRepo{
		byID:    make(map[uuid.UUID]domain.User),
		byEmail: make(map[string]uuid.UUID),
	}
}

func (r *UserRepo) Insert(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byEmail[u.Email]; ok {
		return usecase.ErrEmailExists
	}
	r.byID[u.ID] = *u
	r.byEmail[u.Email] = u.ID
	return nil
}

func (r *UserRepo) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byEmail[email]
	if !ok {
		return nil, usecase.ErrUserNotFound
	}
	u := r.byID[id]
	return &u, nil
}

func (r *UserRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.byID[id]
	if !ok {
		return nil, usecase.ErrUserNotFound
	}
	return &u, nil
}

func (r *UserRepo) SetVerified(_ context.Context, id uuid.UUID, verified bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.byID[id]
	if !ok {
		return usecase.ErrUserNotFound
	}
	u.Verified = verified
	r.byID[id] = u
	return nil
}

func (r *UserRepo) SetRole(_ context.Context, id uuid.UUID, role domain.Role) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.byID[id]
	if !ok {
		return usecase.ErrUserNotFound
	}
	u.Role = role
	r.byID[id] = u
	return nil
}

// Clock is a fixed usecase.Clock for deterministic tests.
type Clock struct{ T time.Time }

func (c Clock) Now() time.Time { return c.T }
