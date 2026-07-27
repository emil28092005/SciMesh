// Package auth holds the cryptographic adapters — password hashing and JWT
// signing/verification. They implement use-case ports and keep bcrypt and the
// JWT library out of the domain and use-case layers.
package auth

import "golang.org/x/crypto/bcrypt"

// Hasher turns plaintext passwords into storable hashes and checks them back.
type Hasher struct {
	cost int
}

// NewHasher builds a Hasher. A cost of 0 uses bcrypt's default work factor.
func NewHasher(cost int) Hasher {
	if cost == 0 {
		cost = bcrypt.DefaultCost
	}
	return Hasher{cost: cost}
}

// Hash returns the bcrypt hash of password. The salt and the cost are embedded
// in the returned string, so nothing else needs to be stored alongside it.
//
// bcrypt silently ignores input past 72 bytes; the use case rejects longer
// passwords before reaching here so a truncated tail never becomes a security
// surprise.
func (h Hasher) Hash(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Compare reports whether password matches the stored hash. It returns a
// non-nil error (bcrypt.ErrMismatchedHashAndPassword) on any mismatch, which
// the caller collapses into a generic authentication failure.
func (h Hasher) Compare(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
