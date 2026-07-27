package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// workerKeyLabel makes a key self-describing when it turns up in a log or an
	// env var, and lets a client sanity-check the shape before exchanging it.
	workerKeyLabel = "scimesh_wk_live_"
	// workerKeyRandomBytes is the entropy behind the secret. 24 bytes (192 bits)
	// is far beyond guessable, which is why the stored hash needs no salt.
	workerKeyRandomBytes = 24
	// workerKeyPrefixChars is how much of the random tail we keep, alongside the
	// label, as the non-secret identifier shown in the UI.
	workerKeyPrefixChars = 8
	// workerKeyNameMax caps the user-supplied label.
	workerKeyNameMax = 100
	// workerKeyDefaultName is used when the caller supplies no label.
	workerKeyDefaultName = "my machine"
)

// WorkerKey is a long-lived, per-user credential for running a worker. The
// secret itself is never stored — only TokenHash — so the plaintext returned by
// NewWorkerKey is the one and only chance to show it to the user.
type WorkerKey struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Name       string
	TokenHash  string
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// NewWorkerKey mints a key for a user and returns both the entity (carrying only
// the hash) and the one-time plaintext to hand back to the caller. The label is
// trimmed and defaulted; an over-long one is rejected.
func NewWorkerKey(userID uuid.UUID, name string, now time.Time) (*WorkerKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = workerKeyDefaultName
	}
	if len(name) > workerKeyNameMax {
		return nil, "", ErrWorkerKeyNameTooLong
	}

	b := make([]byte, workerKeyRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, "", err
	}
	// URL-safe, unpadded: the key rides in env vars and shell commands, so it
	// must contain no '=', '+', or '/' that a shell might mangle.
	raw := workerKeyLabel + base64.RawURLEncoding.EncodeToString(b)

	key := &WorkerKey{
		ID:        uuid.New(),
		UserID:    userID,
		Name:      name,
		TokenHash: HashWorkerKey(raw),
		Prefix:    raw[:len(workerKeyLabel)+workerKeyPrefixChars],
		CreatedAt: now,
	}
	return key, raw, nil
}

// HashWorkerKey returns the hex SHA-256 of a presented key. Exchange hashes the
// incoming key the same way and looks the row up by it, so the plaintext never
// has to be compared directly.
func HashWorkerKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Revoked reports whether the key has been retired and must no longer exchange.
func (k *WorkerKey) Revoked() bool { return k.RevokedAt != nil }
