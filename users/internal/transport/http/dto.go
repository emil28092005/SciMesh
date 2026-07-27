package http

import (
	"time"

	"github.com/emil28092005/SciMesh/users/internal/domain"
)

// registerRequest / loginRequest are the JSON bodies clients POST. Kept separate
// from the domain so the wire format can evolve without touching the entity.
type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// userResponse is the public view of a user. It never carries the password hash.
type userResponse struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Verified  bool   `json:"verified"`
	CreatedAt string `json:"created_at"`
}

type loginResponse struct {
	Token string       `json:"token"`
	User  userResponse `json:"user"`
}

func toUserResponse(u *domain.User) userResponse {
	return userResponse{
		ID:        u.ID.String(),
		Email:     u.Email,
		Role:      string(u.Role),
		Verified:  u.Verified,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// createWorkerKeyRequest is the body for minting a worker key. Name is an
// optional human label; the domain defaults it when blank.
type createWorkerKeyRequest struct {
	Name string `json:"name"`
}

// exchangeWorkerKeyRequest trades a worker key for a short-lived JWT.
type exchangeWorkerKeyRequest struct {
	Key string `json:"key"`
}

type exchangeWorkerKeyResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expires_in"`
}

// workerKeyResponse is the public view of a key. It never carries the secret —
// only the non-secret prefix used to identify a row.
type workerKeyResponse struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

// createdWorkerKeyResponse extends the public view with the one-time plaintext,
// returned only from the create call and never again.
type createdWorkerKeyResponse struct {
	workerKeyResponse
	Key string `json:"key"`
}

type workerKeysResponse struct {
	WorkerKeys []workerKeyResponse `json:"worker_keys"`
}

func toWorkerKeyResponse(k *domain.WorkerKey) workerKeyResponse {
	resp := workerKeyResponse{
		ID:        k.ID.String(),
		Name:      k.Name,
		Prefix:    k.Prefix,
		CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339),
	}
	if k.LastUsedAt != nil {
		resp.LastUsedAt = k.LastUsedAt.UTC().Format(time.RFC3339)
	}
	return resp
}
