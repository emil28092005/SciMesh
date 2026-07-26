package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/users/internal/domain"
)

// Claims is the payload of a signed token. Subject (from RegisteredClaims) is
// the user id — it becomes the coordinator's jobs.owner_id; Role drives
// authorization. Both services verify this token locally with the shared HS256
// secret, so no runtime call back to the userservice is ever needed.
type Claims struct {
	Role domain.Role `json:"role"`
	jwt.RegisteredClaims
}

// Issuer signs and verifies tokens with a shared HS256 secret.
type Issuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewIssuer builds an Issuer. now defaults to time.Now when nil; tests inject a
// fixed clock to make expiry deterministic.
func NewIssuer(secret string, ttl time.Duration, now func() time.Time) Issuer {
	if now == nil {
		now = time.Now
	}
	return Issuer{secret: []byte(secret), ttl: ttl, now: now}
}

// Issue returns a signed token for the user, valid for the configured TTL.
func (i Issuer) Issue(userID uuid.UUID, role domain.Role) (string, error) {
	now := i.now()
	claims := Claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
}

// Verify checks the signature and expiry and returns the claims. It pins the
// algorithm to HMAC, rejecting a token that asks for "none" or an RS256 public
// key — the classic algorithm-substitution attack against naive verifiers.
func (i Issuer) Verify(token string) (*Claims, error) {
	var claims Claims
	_, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return i.secret, nil
	})
	if err != nil {
		return nil, err
	}
	return &claims, nil
}
