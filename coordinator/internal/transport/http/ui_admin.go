package http

import (
	"context"
	"net/http"
	"net/url"
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

	status, err := s.callUserserviceAuthed(r.Context(), http.MethodPost, "/users/"+userID+"/"+action, c.Value)
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
func (s *Server) callUserserviceAuthed(ctx context.Context, method, path, bearer string) (int, error) {
	// path is not attacker-controlled: the caller composes it only from a
	// uuid-validated id and an action from a fixed whitelist, and the host is
	// the operator-configured userservice — so the SSRF taint gosec sees here
	// cannot reach an arbitrary destination.
	req, err := http.NewRequestWithContext(ctx, method, s.userserviceURL+path, nil) //nolint:gosec // G704: path is validated, host is config
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)

	resp, err := s.httpClient.Do(req) //nolint:gosec // G704: see above
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}
