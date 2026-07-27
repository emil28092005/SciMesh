package http

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/users/internal/domain"
	"github.com/emil28092005/SciMesh/users/internal/usecase"
)

// Handlers holds the use cases each endpoint drives.
type Handlers struct {
	register          *usecase.Register
	login             *usecase.Login
	setVerified       *usecase.SetVerified
	setRole           *usecase.SetRole
	createWorkerKey   *usecase.CreateWorkerKey
	listWorkerKeys    *usecase.ListWorkerKeys
	revokeWorkerKey   *usecase.RevokeWorkerKey
	exchangeWorkerKey *usecase.ExchangeWorkerKey
	users             usecase.UserRepository
	log               *slog.Logger
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

// handleSetVerified grants (verified=true) or revokes (false) the trusted-
// contributor badge for the user in the path. Admin-only; the withAdmin
// middleware has already enforced the role by the time this runs.
func (h *Handlers) handleSetVerified(verified bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error:     "invalid user id",
				RequestID: requestIDFrom(r.Context()),
			})
			return
		}
		if err := h.setVerified.Execute(r.Context(), id, verified); err != nil {
			writeError(w, r, h.log, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleSetRole promotes (admin) or demotes (user) the user in the path. Admin-
// only; the withAdmin middleware has already enforced the caller's role.
func (h *Handlers) handleSetRole(role domain.Role) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error:     "invalid user id",
				RequestID: requestIDFrom(r.Context()),
			})
			return
		}
		if err := h.setRole.Execute(r.Context(), id, role); err != nil {
			writeError(w, r, h.log, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleCreateWorkerKey mints a long-lived worker key for the authenticated
// caller and returns it once, plaintext included. The user copies it into their
// worker's SCIMESH_WORKER_KEY; it is never retrievable again.
func (h *Handlers) handleCreateWorkerKey(w http.ResponseWriter, r *http.Request) {
	id, ok := userIDFrom(r.Context())
	if !ok {
		unauthorized(w, r)
		return
	}
	var req createWorkerKeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	key, raw, err := h.createWorkerKey.Execute(r.Context(), id, req.Name)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, createdWorkerKeyResponse{
		workerKeyResponse: toWorkerKeyResponse(key),
		Key:               raw,
	})
}

// handleListWorkerKeys returns the caller's live keys (no secrets) for display
// and revocation.
func (h *Handlers) handleListWorkerKeys(w http.ResponseWriter, r *http.Request) {
	id, ok := userIDFrom(r.Context())
	if !ok {
		unauthorized(w, r)
		return
	}
	keys, err := h.listWorkerKeys.Execute(r.Context(), id)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	out := make([]workerKeyResponse, 0, len(keys))
	for _, k := range keys {
		out = append(out, toWorkerKeyResponse(k))
	}
	writeJSON(w, http.StatusOK, workerKeysResponse{WorkerKeys: out})
}

// handleRevokeWorkerKey retires one of the caller's keys. The repository scopes
// the delete to the owner, so a mismatched id is a clean 404, not another user's
// key.
func (h *Handlers) handleRevokeWorkerKey(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFrom(r.Context())
	if !ok {
		unauthorized(w, r)
		return
	}
	keyID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error:     "invalid worker key id",
			RequestID: requestIDFrom(r.Context()),
		})
		return
	}
	if err := h.revokeWorkerKey.Execute(r.Context(), userID, keyID); err != nil {
		writeError(w, r, h.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleExchangeWorkerKey trades a worker key for a short-lived JWT. It is
// unauthenticated: the key itself is the credential. A worker calls this on
// startup and again to refresh before the JWT expires.
func (h *Handlers) handleExchangeWorkerKey(w http.ResponseWriter, r *http.Request) {
	var req exchangeWorkerKeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	token, expiresIn, err := h.exchangeWorkerKey.Execute(r.Context(), req.Key)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, exchangeWorkerKeyResponse{Token: token, ExpiresIn: expiresIn})
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
