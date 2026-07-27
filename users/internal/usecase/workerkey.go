package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/users/internal/domain"
)

// CreateWorkerKey mints a long-lived worker key for a user and returns the
// one-time plaintext to show once.
type CreateWorkerKey struct {
	keys  WorkerKeyRepository
	clock Clock
}

func NewCreateWorkerKey(keys WorkerKeyRepository, clock Clock) *CreateWorkerKey {
	return &CreateWorkerKey{keys: keys, clock: clock}
}

// Execute returns the stored key (hash only) and the plaintext secret. The
// secret is never persisted, so this is the sole moment it can be surfaced.
func (uc *CreateWorkerKey) Execute(ctx context.Context, userID uuid.UUID, name string) (*domain.WorkerKey, string, error) {
	key, raw, err := domain.NewWorkerKey(userID, name, uc.clock.Now())
	if err != nil {
		return nil, "", err
	}
	if err := uc.keys.Insert(ctx, key); err != nil {
		return nil, "", err
	}
	return key, raw, nil
}

// ListWorkerKeys returns a user's live keys for display and management.
type ListWorkerKeys struct {
	keys WorkerKeyRepository
}

func NewListWorkerKeys(keys WorkerKeyRepository) *ListWorkerKeys {
	return &ListWorkerKeys{keys: keys}
}

func (uc *ListWorkerKeys) Execute(ctx context.Context, userID uuid.UUID) ([]*domain.WorkerKey, error) {
	return uc.keys.ListByUser(ctx, userID)
}

// RevokeWorkerKey retires one of the caller's keys.
type RevokeWorkerKey struct {
	keys WorkerKeyRepository
}

func NewRevokeWorkerKey(keys WorkerKeyRepository) *RevokeWorkerKey {
	return &RevokeWorkerKey{keys: keys}
}

func (uc *RevokeWorkerKey) Execute(ctx context.Context, userID, id uuid.UUID) error {
	return uc.keys.Revoke(ctx, id, userID)
}

// ExchangeWorkerKey trades a valid worker key for a short-lived JWT. The JWT
// carries the owner's current role and verified flag, so a worker that refreshes
// after an admin verifies the owner picks up the upgraded trust on its next
// registration.
type ExchangeWorkerKey struct {
	keys   WorkerKeyRepository
	users  UserRepository
	tokens TokenIssuer
	ttl    time.Duration
}

func NewExchangeWorkerKey(keys WorkerKeyRepository, users UserRepository, tokens TokenIssuer, ttl time.Duration) *ExchangeWorkerKey {
	return &ExchangeWorkerKey{keys: keys, users: users, tokens: tokens, ttl: ttl}
}

// Execute returns a signed token and its lifetime in seconds. Every failure to
// resolve the key to a usable owner collapses to ErrInvalidWorkerKey so a caller
// cannot tell an unknown key from a revoked one or a deleted owner.
func (uc *ExchangeWorkerKey) Execute(ctx context.Context, rawKey string) (string, int, error) {
	if rawKey == "" {
		return "", 0, ErrInvalidWorkerKey
	}

	key, err := uc.keys.GetActiveByHash(ctx, domain.HashWorkerKey(rawKey))
	if err != nil {
		if errors.Is(err, ErrWorkerKeyNotFound) {
			return "", 0, ErrInvalidWorkerKey
		}
		return "", 0, err
	}

	u, err := uc.users.GetByID(ctx, key.UserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return "", 0, ErrInvalidWorkerKey
		}
		return "", 0, err
	}

	token, err := uc.tokens.Issue(u)
	if err != nil {
		return "", 0, err
	}

	// Best-effort: a failed timestamp update must not sink an otherwise valid
	// exchange the worker depends on to keep running.
	_ = uc.keys.TouchLastUsed(ctx, key.ID)

	return token, int(uc.ttl.Seconds()), nil
}
