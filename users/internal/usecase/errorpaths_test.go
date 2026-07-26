package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/users/internal/domain"
	"github.com/emil28092005/SciMesh/users/internal/usecase"
)

// These stubs let a test inject failures the happy-path memstore never produces,
// so the use cases' error branches are exercised too.

var errBoom = errors.New("boom")

type stubRepo struct {
	getByEmail func() (*domain.User, error)
	insert     func() error
}

func (s stubRepo) Insert(context.Context, *domain.User) error { return s.insert() }
func (s stubRepo) GetByEmail(context.Context, string) (*domain.User, error) {
	return s.getByEmail()
}
func (s stubRepo) GetByID(context.Context, uuid.UUID) (*domain.User, error) {
	return nil, usecase.ErrUserNotFound
}
func (s stubRepo) SetVerified(context.Context, uuid.UUID, bool) error {
	return usecase.ErrUserNotFound
}

type stubHasher struct {
	hashErr    error
	compareErr error
}

func (s stubHasher) Hash(string) (string, error) {
	if s.hashErr != nil {
		return "", s.hashErr
	}
	return "hashed", nil
}
func (s stubHasher) Compare(string, string) error { return s.compareErr }

type stubIssuer struct{ err error }

func (s stubIssuer) Issue(*domain.User) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return "token", nil
}

func TestRegisterPropagatesHasherError(t *testing.T) {
	clk := stubClock{time.Now()}
	reg := usecase.NewRegister(stubRepo{}, stubHasher{hashErr: errBoom}, clk)

	_, err := reg.Execute(context.Background(), "a@b.com", "password123")
	if !errors.Is(err, errBoom) {
		t.Errorf("got %v, want errBoom", err)
	}
}

func TestRegisterPropagatesInsertError(t *testing.T) {
	clk := stubClock{time.Now()}
	repo := stubRepo{insert: func() error { return errBoom }}
	reg := usecase.NewRegister(repo, stubHasher{}, clk)

	_, err := reg.Execute(context.Background(), "a@b.com", "password123")
	if !errors.Is(err, errBoom) {
		t.Errorf("got %v, want errBoom", err)
	}
}

func TestLoginPropagatesRepoError(t *testing.T) {
	// A non-ErrUserNotFound repo error must surface as-is, not be masked as
	// ErrInvalidCredentials.
	repo := stubRepo{getByEmail: func() (*domain.User, error) { return nil, errBoom }}
	login := usecase.NewLogin(repo, stubHasher{}, stubIssuer{})

	_, _, err := login.Execute(context.Background(), "a@b.com", "password123")
	if !errors.Is(err, errBoom) {
		t.Errorf("got %v, want errBoom", err)
	}
}

func TestLoginPropagatesIssuerError(t *testing.T) {
	repo := stubRepo{getByEmail: func() (*domain.User, error) {
		return &domain.User{ID: uuid.New(), Email: "a@b.com", Role: domain.RoleUser}, nil
	}}
	// Hasher accepts the password (nil compareErr) so we reach token issuance.
	login := usecase.NewLogin(repo, stubHasher{}, stubIssuer{err: errBoom})

	_, _, err := login.Execute(context.Background(), "a@b.com", "password123")
	if !errors.Is(err, errBoom) {
		t.Errorf("got %v, want errBoom", err)
	}
}

type stubClock struct{ t time.Time }

func (c stubClock) Now() time.Time { return c.t }
