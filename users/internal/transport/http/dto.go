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
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339),
	}
}
