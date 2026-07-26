// Package token verifies the HS256 JWTs minted by the userservice. The
// coordinator only ever *verifies* — it never issues — so this is a deliberately
// small counterpart to the userservice's issuer. Verification is local: the
// shared secret is enough, with no runtime call back to the userservice.
package token

import (
	"fmt"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims is the subset of a userservice token the coordinator cares about.
type Claims struct {
	UserID   uuid.UUID
	Role     string
	Verified bool
}

// Verifier checks tokens against the shared HS256 secret.
type Verifier struct {
	secret []byte
}

// NewVerifier returns a Verifier, or nil when secret is empty — a nil Verifier
// means user-JWT auth is disabled and only the shared service token is accepted.
func NewVerifier(secret string) *Verifier {
	if secret == "" {
		return nil
	}
	return &Verifier{secret: []byte(secret)}
}

type claims struct {
	Role     string `json:"role"`
	Verified bool   `json:"verified"`
	jwt.RegisteredClaims
}

// Verify checks the signature and expiry and returns the identity. It pins the
// algorithm to HMAC, rejecting a token that asks for "none" or an RS256 public
// key — the classic algorithm-substitution attack.
func (v *Verifier) Verify(raw string) (Claims, error) {
	var c claims
	_, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return v.secret, nil
	})
	if err != nil {
		return Claims{}, err
	}
	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return Claims{}, fmt.Errorf("token subject is not a uuid: %w", err)
	}
	return Claims{UserID: id, Role: c.Role, Verified: c.Verified}, nil
}
