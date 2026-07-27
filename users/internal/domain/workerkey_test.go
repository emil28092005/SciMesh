package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/users/internal/domain"
)

func TestNewWorkerKeyShape(t *testing.T) {
	owner := uuid.New()
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)

	key, raw, err := domain.NewWorkerKey(owner, "home-desktop", now)
	if err != nil {
		t.Fatalf("NewWorkerKey: %v", err)
	}
	if !strings.HasPrefix(raw, "scimesh_wk_live_") {
		t.Errorf("raw key has no recognisable label: %q", raw)
	}
	if key.TokenHash != domain.HashWorkerKey(raw) {
		t.Error("stored hash does not match the plaintext")
	}
	if key.TokenHash == raw || strings.Contains(key.TokenHash, raw) {
		t.Error("plaintext leaked into the stored hash")
	}
	if !strings.HasPrefix(raw, key.Prefix) {
		t.Errorf("prefix %q is not a leading slice of the key", key.Prefix)
	}
	if key.UserID != owner || key.CreatedAt != now || key.Revoked() {
		t.Errorf("unexpected key metadata: %+v", key)
	}
}

func TestNewWorkerKeyDefaultsBlankName(t *testing.T) {
	key, _, err := domain.NewWorkerKey(uuid.New(), "   ", time.Now())
	if err != nil {
		t.Fatalf("NewWorkerKey: %v", err)
	}
	if key.Name == "" {
		t.Error("blank name was not defaulted")
	}
}

func TestNewWorkerKeyRejectsLongName(t *testing.T) {
	_, _, err := domain.NewWorkerKey(uuid.New(), strings.Repeat("x", 101), time.Now())
	if !errors.Is(err, domain.ErrWorkerKeyNameTooLong) {
		t.Errorf("got %v, want ErrWorkerKeyNameTooLong", err)
	}
}

func TestNewWorkerKeyUniquePerCall(t *testing.T) {
	a, rawA, _ := domain.NewWorkerKey(uuid.New(), "a", time.Now())
	b, rawB, _ := domain.NewWorkerKey(uuid.New(), "b", time.Now())
	if rawA == rawB || a.TokenHash == b.TokenHash || a.ID == b.ID {
		t.Error("two keys collided; generation is not random")
	}
}
