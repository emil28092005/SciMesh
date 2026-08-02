// Package memstore provides in-memory implementations of the usecase ports for
// fast, deterministic tests that need no database.
package memstore

import (
	"context"
	"sort"
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

// ListUsers returns every account, oldest first. It copies, so callers cannot
// corrupt the store through the returned slice.
func (r *UserRepo) ListUsers(_ context.Context) ([]*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	users := make([]*domain.User, 0, len(r.byID))
	for _, u := range r.byID {
		copy := u
		users = append(users, &copy)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].CreatedAt.Before(users[j].CreatedAt) })
	return users, nil
}

// Clock is a fixed usecase.Clock for deterministic tests.
type Clock struct{ T time.Time }

func (c Clock) Now() time.Time { return c.T }

// WorkerKeyRepo is an in-memory usecase.WorkerKeyRepository.
type WorkerKeyRepo struct {
	mu   sync.Mutex
	keys map[uuid.UUID]*domain.WorkerKey
}

func NewWorkerKeyRepo() *WorkerKeyRepo {
	return &WorkerKeyRepo{keys: map[uuid.UUID]*domain.WorkerKey{}}
}

func (r *WorkerKeyRepo) Insert(_ context.Context, k *domain.WorkerKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[k.ID] = k
	return nil
}

func (r *WorkerKeyRepo) ListByUser(_ context.Context, userID uuid.UUID) ([]*domain.WorkerKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.WorkerKey
	for _, k := range r.keys {
		if k.UserID == userID && !k.Revoked() {
			out = append(out, k)
		}
	}
	return out, nil
}

func (r *WorkerKeyRepo) ListAll(_ context.Context) ([]*domain.WorkerKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*domain.WorkerKey, 0, len(r.keys))
	for _, k := range r.keys {
		out = append(out, k)
	}
	return out, nil
}

func (r *WorkerKeyRepo) GetActiveByHash(_ context.Context, tokenHash string) (*domain.WorkerKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.keys {
		if k.TokenHash == tokenHash && !k.Revoked() {
			return k, nil
		}
	}
	return nil, usecase.ErrWorkerKeyNotFound
}

func (r *WorkerKeyRepo) Revoke(_ context.Context, id, userID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.keys[id]
	if !ok || k.UserID != userID || k.Revoked() {
		return usecase.ErrWorkerKeyNotFound
	}
	now := time.Now()
	k.RevokedAt = &now
	return nil
}

func (r *WorkerKeyRepo) RevokeAny(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.keys[id]
	if !ok || k.Revoked() {
		return usecase.ErrWorkerKeyNotFound
	}
	now := time.Now()
	k.RevokedAt = &now
	return nil
}

func (r *WorkerKeyRepo) TouchLastUsed(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k, ok := r.keys[id]; ok {
		now := time.Now()
		k.LastUsedAt = &now
	}
	return nil
}
