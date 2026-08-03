package http

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/authctx"
	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// handleUIAddWorker renders the "add your machine" page: instructions, the
// user's existing worker keys, and a ready-to-run command carrying a freshly
// minted key. All key operations happen client-side against the JSON endpoints
// below; this handler only supplies the browser-facing URLs.
func (s *Server) handleUIAddWorker(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"CoordinatorURL": s.publicCoordinatorURL,
		"UserserviceURL": s.publicUserserviceURL,
	}
	if req, ok := authctx.From(r.Context()); ok {
		data["Session"] = &usecase.SessionView{Role: req.Role, Verified: req.Verified}
	}
	s.renderUI(w, "add-worker.html", data)
}

// handleUIWorkerKeysList proxies the caller's live worker keys from the
// userservice, forwarding their session token.
func (s *Server) handleUIWorkerKeysList(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not signed in"})
		return
	}
	status, body, err := s.callUserserviceAuthed(r.Context(), http.MethodGet, "/worker-keys", c.Value)
	if err != nil {
		s.log.Error("worker-keys list proxy", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "userservice unavailable"})
		return
	}
	proxyJSON(w, status, body)
}

// handleUIWorkerKeyCreate mints a new worker key via the userservice and returns
// its response — including the one-time plaintext key — straight to the browser.
func (s *Server) handleUIWorkerKeyCreate(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not signed in"})
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<12))
	if len(body) == 0 {
		body = []byte("{}")
	}
	status, respBody, err := s.callUserserviceAuthedBody(r.Context(), http.MethodPost, "/worker-keys", c.Value, body)
	if err != nil {
		s.log.Error("worker-key create proxy", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "userservice unavailable"})
		return
	}
	proxyJSON(w, status, respBody)
}

// handleUIWorkerKeyRevoke retires one of the caller's keys via the userservice.
// The id is validated as a UUID so the proxied path can never be attacker-shaped.
func (s *Server) handleUIWorkerKeyRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid worker key id"})
		return
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not signed in"})
		return
	}
	status, _, err := s.callUserserviceAuthed(r.Context(), http.MethodDelete, "/worker-keys/"+id, c.Value)
	if err != nil {
		s.log.Error("worker-key revoke proxy", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "userservice unavailable"})
		return
	}
	w.WriteHeader(status)
}

// proxyJSON forwards a userservice JSON response verbatim, preserving its status.
func proxyJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// callUserserviceAuthedBody is callUserserviceAuthed with a JSON request body,
// used for the create call. Kept separate so the bodyless admin/profile callers
// stay unchanged.
func (s *Server) callUserserviceAuthedBody(ctx context.Context, method, path, bearer string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.userserviceURL+path, bytes.NewReader(body)) //nolint:gosec // G704: path is a fixed literal, host is config
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req) //nolint:gosec // G704: see above
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, respBody, nil
}

// handleWorkerTokenExchangeProxy forwards a worker-key exchange to the
// userservice. The key itself is the credential, so this route is public —
// exactly like the userservice's own endpoint. In `serve` mode the embedded
// userservice binds loopback only, so workers need the coordinator to front
// the exchange for them.
func (s *Server) handleWorkerTokenExchangeProxy(w http.ResponseWriter, r *http.Request) {
	if s.userserviceURL == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.userserviceURL+"/worker-tokens/exchange", bytes.NewReader(body)) //nolint:gosec // G704: path is fixed, host is config
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req) //nolint:gosec // G704: see above
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	proxyJSON(w, resp.StatusCode, respBody)
}
