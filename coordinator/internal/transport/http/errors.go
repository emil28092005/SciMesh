package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// maxJSONBody caps a JSON request body. The DTOs are tiny; anything larger is a
// mistake or an attack, and must not be read into memory unbounded.
const maxJSONBody = 1 << 20 // 1 MiB

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxJSONBody))
	// Reject unknown fields: silently ignoring a misspelled "worker_ID" would
	// surface later as a baffling validation failure.
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// writeError translates domain errors into status codes. This mapping is the
// only place in the codebase that knows HTTP status codes exist — the inner
// layers speak only in business terms.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	reqID := requestIDFrom(r.Context())

	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		status = http.StatusBadRequest
	case errors.Is(err, domain.ErrJobNotFound), errors.Is(err, domain.ErrTaskNotFound),
		errors.Is(err, domain.ErrWorkerNotFound), errors.Is(err, domain.ErrArtifactNotFound):
		status = http.StatusNotFound
	case errors.Is(err, domain.ErrLeaseConflict),
		errors.Is(err, domain.ErrStaleAttempt),
		errors.Is(err, domain.ErrResultConflict),
		errors.Is(err, domain.ErrTaskNotLeased):
		status = http.StatusConflict
	case errors.Is(err, usecase.ErrNotImplemented):
		status = http.StatusNotImplemented
	}

	// 501 says "this endpoint has no implementation yet" — that leaks nothing and
	// is far more useful than a generic failure, which sent one debugging session
	// hunting a database problem that did not exist.
	if status == http.StatusNotImplemented {
		writeJSON(w, status, errorResponse{Error: "not implemented", RequestID: reqID})
		return
	}

	if status >= 500 {
		// Never echo an internal error: it can carry table names, query
		// fragments, and values. The request ID is the bridge to the logs.
		s.log.Error("request failed", "request_id", reqID, "path", r.URL.Path, "err", err)
		writeJSON(w, status, errorResponse{Error: "internal error", RequestID: reqID})
		return
	}
	writeJSON(w, status, errorResponse{Error: err.Error(), RequestID: reqID})
}
