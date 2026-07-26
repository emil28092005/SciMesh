package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewUserNormalisesAndValidates(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	u, err := NewUser("  Bob@Example.COM ", "hashed", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Email != "bob@example.com" {
		t.Errorf("email not normalised: got %q", u.Email)
	}
	if u.Role != RoleUser {
		t.Errorf("new user must default to RoleUser, got %q", u.Role)
	}
	if u.ID == uuid.Nil {
		t.Error("new user must get an id")
	}
	if !u.CreatedAt.Equal(now) || !u.UpdatedAt.Equal(now) {
		t.Error("timestamps not set from clock")
	}
}

func TestNewUserRejectsBadInput(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		email   string
		hash    string
		wantErr error
	}{
		{"empty email", "", "h", ErrEmptyEmail},
		{"no domain", "bob", "h", ErrInvalidEmail},
		{"name form", "Bob <bob@x.com>", "h", ErrInvalidEmail},
		{"empty hash", "bob@x.com", "", ErrEmptyPasswordHash},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewUser(tc.email, tc.hash, now)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestRoleValid(t *testing.T) {
	if !RoleUser.Valid() || !RoleAdmin.Valid() {
		t.Error("user and admin must be valid")
	}
	if Role("root").Valid() {
		t.Error("unknown role must be invalid")
	}
}
