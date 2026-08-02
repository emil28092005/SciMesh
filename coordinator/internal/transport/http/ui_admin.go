package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/authctx"
)

// adminUserActions are the userservice endpoints the admin panel may invoke, by
// their path suffix. A whitelist so a crafted form can never proxy an arbitrary
// path.
var adminUserActions = map[string]bool{
	"promote":  true,
	"demote":   true,
	"verify":   true,
	"unverify": true,
}

// requireAdmin gates a route on the session caller being an admin. It runs
// inside withUISession, which has already stamped the requester. A non-admin is
// sent back to the dashboard rather than shown the panel.
func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if req, ok := authctx.From(r.Context()); !ok || !req.IsAdmin() {
			http.Redirect(w, r, "/ui", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleUIAdmin(w http.ResponseWriter, r *http.Request) {
	role := ""
	if req, ok := authctx.From(r.Context()); ok {
		role = req.Role
	}
	s.renderUI(w, "admin.html", map[string]any{
		"Role":  role,
		"Msg":   r.URL.Query().Get("msg"),
		"Error": r.URL.Query().Get("error"),
	})
}

// handleUIAdminUserAction proxies a user-management action to the userservice,
// forwarding the admin's session token so the userservice re-checks the role.
// The user id and action come from the form, so a single static form action can
// drive every operation.
func (s *Server) handleUIAdminUserAction(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(r.FormValue("user_id"))
	action := r.FormValue("action")

	if !adminUserActions[action] {
		http.Redirect(w, r, "/ui/admin?error=unknown+action", http.StatusSeeOther)
		return
	}
	if _, err := uuid.Parse(userID); err != nil {
		http.Redirect(w, r, "/ui/admin?error=invalid+user+id", http.StatusSeeOther)
		return
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		redirectToLogin(w, r)
		return
	}

	status, _, err := s.callUserserviceAuthed(r.Context(), http.MethodPost, "/users/"+userID+"/"+action, c.Value)
	if err != nil {
		s.log.Error("admin action proxy", "err", err, "action", action)
		http.Redirect(w, r, "/ui/admin?error=service+unavailable", http.StatusSeeOther)
		return
	}
	switch status {
	case http.StatusNoContent:
		http.Redirect(w, r, "/ui/admin?msg="+url.QueryEscape(action+" applied"), http.StatusSeeOther)
	case http.StatusNotFound:
		http.Redirect(w, r, "/ui/admin?error=user+not+found", http.StatusSeeOther)
	case http.StatusForbidden, http.StatusUnauthorized:
		http.Redirect(w, r, "/ui/admin?error=not+authorized", http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/ui/admin?error=action+failed", http.StatusSeeOther)
	}
}

// callUserserviceAuthed makes an authenticated call to the userservice, passing
// the caller's JWT through as a bearer token. Used for admin actions; login and
// registration use the unauthenticated callUserservice.
func (s *Server) callUserserviceAuthed(ctx context.Context, method, path, bearer string) (int, []byte, error) {
	// path is not attacker-controlled: the caller composes it only from a
	// uuid-validated id and an action from a fixed whitelist, and the host is
	// the operator-configured userservice — so the SSRF taint gosec sees here
	// cannot reach an arbitrary destination.
	req, err := http.NewRequestWithContext(ctx, method, s.userserviceURL+path, nil) //nolint:gosec // G704: path is validated, host is config
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)

	resp, err := s.httpClient.Do(req) //nolint:gosec // G704: see above
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// handleUIAdminSystemJSON serves the admin "System" page: process info,
// storage figures and health. Admin-only via the route chain.
func (s *Server) handleUIAdminSystemJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	view, err := s.uc.Admin.System(ctx)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleUIAdminJobsJSON serves one page of the admin jobs table. The owner
// emails are resolved from the userservice when it is reachable; the resolver
// failing is not fatal (cards fall back to short ids).
func (s *Server) handleUIAdminJobsJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	view, err := s.uc.Admin.Jobs(ctx, status, page, perPage, s.adminOwnerEmails(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleUIAdminMetricsJSON serves the admin "Metrics" page.
func (s *Server) handleUIAdminMetricsJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	view, err := s.uc.Admin.Metrics(ctx)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// adminOwnerEmails resolves job owner ids to emails through the userservice,
// which is the only place email addresses live. It never blocks the page on
// failure: an empty map leaves the admin jobs table on short ids.
func (s *Server) adminOwnerEmails(r *http.Request) map[uuid.UUID]string {
	if s.userserviceURL == "" {
		return nil
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	status, body, err := s.callUserserviceAuthed(r.Context(), http.MethodGet, "/users", c.Value)
	if err != nil || status != http.StatusOK {
		return nil
	}
	var users []struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &users); err != nil {
		return nil
	}
	out := make(map[uuid.UUID]string, len(users))
	for _, user := range users {
		id, err := uuid.Parse(user.ID)
		if err != nil {
			continue
		}
		out[id] = user.Email
	}
	return out
}
