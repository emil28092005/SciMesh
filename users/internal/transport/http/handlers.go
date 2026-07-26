package http

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/emil28092005/SciMesh/users/internal/usecase"
)

// Handlers holds the use cases each endpoint drives.
type Handlers struct {
	register *usecase.Register
	login    *usecase.Login
	users    usecase.UserRepository
	log      *slog.Logger
}

// handleHealth is an unauthenticated liveness probe for the container and load
// balancer.
func (h *Handlers) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleRegister creates an account. It returns 201 with the public user view,
// 409 if the email is taken, or 400 on a malformed body / weak password.
func (h *Handlers) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := h.register.Execute(r.Context(), req.Email, req.Password)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, toUserResponse(u))
}

// handleLogin verifies credentials and returns a signed token plus the user.
func (h *Handlers) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	token, u, err := h.login.Execute(r.Context(), req.Email, req.Password)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{Token: token, User: toUserResponse(u)})
}

// handleMe returns the caller's own account, proving the token works end to end.
// It reads the user id the JWT middleware stashed in the context.
func (h *Handlers) handleMe(w http.ResponseWriter, r *http.Request) {
	id, ok := userIDFrom(r.Context())
	if !ok {
		unauthorized(w, r)
		return
	}
	u, err := h.users.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

// decodeJSON reads a size-capped JSON body into dst, rejecting unknown fields.
// It writes a 400 and returns false on any problem, so callers can `if
// !decodeJSON(...) { return }`.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error:     "invalid JSON body",
			RequestID: requestIDFrom(r.Context()),
		})
		return false
	}
	return true
}
