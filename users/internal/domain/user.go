package domain

import (
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

func (r Role) Valid() bool {
	switch r {
	case RoleAdmin, RoleUser:
		return true
	default:
		return false
	}
}

type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	Role         Role
	// Verified marks a trusted contributor whose workers' results the
	// coordinator accepts without quorum. Distinct from Role; granted by an
	// admin, defaults to false.
	Verified  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewUser builds a freshly registered account. It normalises the email and
// enforces every invariant a row must satisfy, so an invalid User cannot be
// constructed. The caller supplies the already-hashed password — hashing is an
// adapter's job, not the domain's.
//
// Registration always produces a plain user; promotion to admin is a manual,
// out-of-band operation, never something a request can trigger.
func NewUser(email, passwordHash string, now time.Time) (*User, error) {
	email = NormalizeEmail(email)
	if err := validateEmail(email); err != nil {
		return nil, err
	}
	if passwordHash == "" {
		return nil, ErrEmptyPasswordHash
	}
	return &User{
		ID:           uuid.New(),
		Email:        email,
		PasswordHash: passwordHash,
		Role:         RoleUser,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// NormalizeEmail lower-cases and trims an address so that "Bob@X.com " and
// "bob@x.com" resolve to the same account. Every lookup and every insert must
// pass through here, matching the ck_users_email_lower database constraint.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validateEmail(email string) error {
	if email == "" {
		return ErrEmptyEmail
	}
	// A minimal shape check, not full RFC 5322: real deliverability is proven by
	// sending mail, not by a regex. mail.ParseAddress also accepts the
	// "Name <addr>" form, so we insist the parsed address equals the input.
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return ErrInvalidEmail
	}
	return nil
}
