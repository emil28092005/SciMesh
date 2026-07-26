package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/emil28092005/SciMesh/users/internal/domain"
	"github.com/emil28092005/SciMesh/users/internal/usecase"
)

// maxJSONBody caps a request body. Credentials are tiny; anything larger is a
// mistake or an attack, so reject it before allocating.
const maxJSONBody = 1 << 20 // 1 MiB

type errorResponse struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError maps a domain or use-case error to an HTTP status and a safe
// message, logging only genuine server faults (5xx). Client errors (4xx) are
// expected and stay out of the error log.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	status, msg := statusForError(err)
	if status >= http.StatusInternalServerError {
		log.Error("request failed",
			"err", err,
			"request_id", requestIDFrom(r.Context()),
			"path", r.URL.Path,
		)
	}
	writeJSON(w, status, errorResponse{Error: msg, RequestID: requestIDFrom(r.Context())})
}

func statusForError(err error) (int, string) {
	switch {
	case errors.Is(err, usecase.ErrEmailExists):
		return http.StatusConflict, "email already registered"
	case errors.Is(err, usecase.ErrInvalidCredentials):
		return http.StatusUnauthorized, "invalid email or password"
	case errors.Is(err, usecase.ErrUserNotFound):
		return http.StatusNotFound, "user not found"
	case errors.Is(err, usecase.ErrPasswordTooShort):
		return http.StatusBadRequest, "password must be at least 8 characters"
	case errors.Is(err, usecase.ErrPasswordTooLong):
		return http.StatusBadRequest, "password must be at most 72 bytes"
	case errors.Is(err, domain.ErrEmptyEmail), errors.Is(err, domain.ErrInvalidEmail):
		return http.StatusBadRequest, "email is not a valid address"
	default:
		// Don't leak internals; the real error is in the log under request_id.
		return http.StatusInternalServerError, "internal error"
	}
}
