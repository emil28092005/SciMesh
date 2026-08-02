package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/auth"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/memstore"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/usecase"
)

// fakeKeyRepo is an in-memory WorkerKeyRepository for the use-case tests.
type fakeKeyRepo struct {
	byHash  map[string]*domain.WorkerKey
	byID    map[uuid.UUID]*domain.WorkerKey
	touched []uuid.UUID
}

func newFakeKeyRepo() *fakeKeyRepo {
	return &fakeKeyRepo{byHash: map[string]*domain.WorkerKey{}, byID: map[uuid.UUID]*domain.WorkerKey{}}
}

func (r *fakeKeyRepo) Insert(_ context.Context, k *domain.WorkerKey) error {
	r.byHash[k.TokenHash] = k
	r.byID[k.ID] = k
	return nil
}

func (r *fakeKeyRepo) ListByUser(_ context.Context, userID uuid.UUID) ([]*domain.WorkerKey, error) {
	var out []*domain.WorkerKey
	for _, k := range r.byID {
		if k.UserID == userID && !k.Revoked() {
			out = append(out, k)
		}
	}
	return out, nil
}

func (r *fakeKeyRepo) GetActiveByHash(_ context.Context, hash string) (*domain.WorkerKey, error) {
	k, ok := r.byHash[hash]
	if !ok || k.Revoked() {
		return nil, usecase.ErrWorkerKeyNotFound
	}
	return k, nil
}

func (r *fakeKeyRepo) Revoke(_ context.Context, id, userID uuid.UUID) error {
	k, ok := r.byID[id]
	if !ok || k.UserID != userID || k.Revoked() {
		return usecase.ErrWorkerKeyNotFound
	}
	now := time.Now()
	k.RevokedAt = &now
	return nil
}

func (r *fakeKeyRepo) TouchLastUsed(_ context.Context, id uuid.UUID) error {
	r.touched = append(r.touched, id)
	return nil
}

func (r *fakeKeyRepo) ListAll(_ context.Context) ([]*domain.WorkerKey, error) {
	out := make([]*domain.WorkerKey, 0, len(r.byID))
	for _, k := range r.byID {
		out = append(out, k)
	}
	return out, nil
}

func (r *fakeKeyRepo) RevokeAny(_ context.Context, id uuid.UUID) error {
	k, ok := r.byID[id]
	if !ok || k.Revoked() {
		return usecase.ErrWorkerKeyNotFound
	}
	now := time.Now()
	k.RevokedAt = &now
	return nil
}

func newKeyFixtures(t *testing.T) (*usecase.CreateWorkerKey, *usecase.ExchangeWorkerKey, *usecase.RevokeWorkerKey, *usecase.ListWorkerKeys, *fakeKeyRepo, *domain.User) {
	t.Helper()
	users := memstore.NewUserRepo()
	hasher := auth.NewHasher(4)
	clk := memstore.Clock{T: time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)}
	issuer := auth.NewIssuer(secret, time.Hour, nil)
	keys := newFakeKeyRepo()

	u, err := usecase.NewRegister(users, hasher, clk).Execute(context.Background(), "worker@example.com", "password123")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	return usecase.NewCreateWorkerKey(keys, clk),
		usecase.NewExchangeWorkerKey(keys, users, issuer, time.Hour),
		usecase.NewRevokeWorkerKey(keys),
		usecase.NewListWorkerKeys(keys),
		keys, u
}

func TestCreateAndExchangeWorkerKey(t *testing.T) {
	create, exchange, _, _, keys, u := newKeyFixtures(t)
	ctx := context.Background()

	key, raw, err := create.Execute(ctx, u.ID, "home-desktop")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if key.Name != "home-desktop" || raw == "" {
		t.Fatalf("unexpected key %+v raw=%q", key, raw)
	}

	token, expiresIn, err := exchange.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if expiresIn != int((time.Hour).Seconds()) {
		t.Errorf("expires_in = %d, want 3600", expiresIn)
	}

	claims, err := auth.NewIssuer(secret, time.Hour, nil).Verify(token)
	if err != nil {
		t.Fatalf("issued token does not verify: %v", err)
	}
	if claims.Subject != u.ID.String() {
		t.Errorf("token sub = %q, want owner %q", claims.Subject, u.ID)
	}
	if len(keys.touched) != 1 || keys.touched[0] != key.ID {
		t.Errorf("exchange did not record last-used, touched=%v", keys.touched)
	}
}

func TestExchangeUnknownKeyIsInvalid(t *testing.T) {
	_, exchange, _, _, _, _ := newKeyFixtures(t)
	if _, _, err := exchange.Execute(context.Background(), "scimesh_wk_live_nope"); !errors.Is(err, usecase.ErrInvalidWorkerKey) {
		t.Errorf("got %v, want ErrInvalidWorkerKey", err)
	}
}

func TestExchangeEmptyKeyIsInvalid(t *testing.T) {
	_, exchange, _, _, _, _ := newKeyFixtures(t)
	if _, _, err := exchange.Execute(context.Background(), ""); !errors.Is(err, usecase.ErrInvalidWorkerKey) {
		t.Errorf("got %v, want ErrInvalidWorkerKey", err)
	}
}

func TestExchangeRevokedKeyIsInvalid(t *testing.T) {
	create, exchange, revoke, _, _, u := newKeyFixtures(t)
	ctx := context.Background()

	key, raw, err := create.Execute(ctx, u.ID, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if err := revoke.Execute(ctx, u.ID, key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, _, err := exchange.Execute(ctx, raw); !errors.Is(err, usecase.ErrInvalidWorkerKey) {
		t.Errorf("revoked key still exchanges: %v", err)
	}
}

func TestRevokeIsScopedToOwner(t *testing.T) {
	create, _, revoke, _, _, u := newKeyFixtures(t)
	ctx := context.Background()

	key, _, err := create.Execute(ctx, u.ID, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	// A different user must not be able to revoke this key.
	if err := revoke.Execute(ctx, uuid.New(), key.ID); !errors.Is(err, usecase.ErrWorkerKeyNotFound) {
		t.Errorf("cross-owner revoke returned %v, want ErrWorkerKeyNotFound", err)
	}
}

func TestListReturnsOnlyLiveKeys(t *testing.T) {
	create, _, revoke, list, _, u := newKeyFixtures(t)
	ctx := context.Background()

	live, _, err := create.Execute(ctx, u.ID, "keep")
	if err != nil {
		t.Fatal(err)
	}
	dead, _, err := create.Execute(ctx, u.ID, "drop")
	if err != nil {
		t.Fatal(err)
	}
	if err := revoke.Execute(ctx, u.ID, dead.ID); err != nil {
		t.Fatal(err)
	}

	got, err := list.Execute(ctx, u.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != live.ID {
		t.Errorf("list = %d keys, want only the live one", len(got))
	}
}
