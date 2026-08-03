package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/authctx"
	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
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
// inside withUISession, which has already stamped the requester. A signed-in
// non-admin is told why (and bounced to the login with the message); an
// unauthenticated caller never gets here — the gate has already sent them to
// the login page with the intended destination.
func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if req, ok := authctx.From(r.Context()); !ok || !req.IsAdmin() {
			target := "/ui/login?error=admin+role+required"
			// Keep the destination so a successful login lands straight back.
			if strings.HasPrefix(r.URL.Path, "/ui/") {
				target += "&next=" + url.QueryEscape(r.URL.Path)
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
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

// handleUIAdminWorkersJSON serves the admin "Workers" page.
func (s *Server) handleUIAdminWorkersJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	view, err := s.uc.Admin.Workers(ctx, s.adminOwnerEmails(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleUIAdminSetTrustJSON flips one worker's trust level.
func (s *Server) handleUIAdminSetTrustJSON(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}
	var body struct {
		Trusted bool `json:"trusted"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	if err := s.uc.Admin.SetTrust(ctx, id, body.Trusted); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUIAdminWorkloadsJSON serves the catalog with persisted enable flags.
func (s *Server) handleUIAdminWorkloadsJSON(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	view, err := s.uc.Admin.Workloads(ctx)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleUIAdminSetWorkloadEnabledJSON flips a workload's enable flag.
func (s *Server) handleUIAdminSetWorkloadEnabledJSON(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	if err := s.uc.Admin.SetWorkloadEnabled(ctx, name, body.Enabled); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUIAdminSettingsJSON serves the read-only cluster settings.
func (s *Server) handleUIAdminSettingsJSON(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.uc.Admin.Settings())
}

// handleUIAdminRevealTokenJSON reveals the shared worker token, auditing the
// reveal. Admin-only via the route chain.
func (s *Server) handleUIAdminRevealTokenJSON(w http.ResponseWriter, r *http.Request) {
	actor := "admin"
	if req, ok := authctx.From(r.Context()); ok {
		actor = req.Role + ":" + req.UserID.String()
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]string{"token": s.uc.Admin.RevealWorkerToken(ctx, actor)})
}

// handleUIAdminUsersJSON serves the account table, proxied from the
// userservice. The userservice projects away password hashes; a failure here
// is a 502 rather than a silent empty table.
func (s *Server) handleUIAdminUsersJSON(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		redirectToLogin(w, r)
		return
	}
	status, body, err := s.callUserserviceAuthed(r.Context(), http.MethodGet, "/users", c.Value)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if status != http.StatusOK {
		writeJSON(w, status, map[string]string{"error": "userservice: unexpected response"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleUIAdminSetUserRoleJSON changes a user's role through the userservice
// promote/demote actions.
func (s *Server) handleUIAdminSetUserRoleJSON(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}
	action := ""
	switch body.Role {
	case "admin":
		action = "promote"
	case "user":
		action = "demote"
	default:
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		redirectToLogin(w, r)
		return
	}
	status, _, err := s.callUserserviceAuthed(r.Context(), http.MethodPost, "/users/"+id.String()+"/"+action, c.Value)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if status != http.StatusNoContent {
		writeJSON(w, status, map[string]string{"error": "userservice: unexpected response"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUIAdminWorkerKeysJSON serves every worker key with its owning user,
// proxied from the userservice.
func (s *Server) handleUIAdminWorkerKeysJSON(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		redirectToLogin(w, r)
		return
	}
	status, body, err := s.callUserserviceAuthed(r.Context(), http.MethodGet, "/worker-keys/all", c.Value)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if status != http.StatusOK {
		writeJSON(w, status, map[string]string{"error": "userservice: unexpected response"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleUIAdminRevokeKeyJSON revokes any worker key through the userservice
// (whose DELETE endpoint already lets an admin revoke keys of any owner).
func (s *Server) handleUIAdminRevokeKeyJSON(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		redirectToLogin(w, r)
		return
	}
	status, _, err := s.callUserserviceAuthed(r.Context(), http.MethodDelete, "/worker-keys/"+id.String(), c.Value)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if status != http.StatusNoContent {
		writeJSON(w, status, map[string]string{"error": "userservice: unexpected response"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// handleUIAdminPruneJSON deletes finished jobs older than the requested
// number of days (with all their artifacts) and reports what was freed.
func (s *Server) handleUIAdminPruneJSON(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OlderThanDays int `json:"older_than_days"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}
	if body.OlderThanDays < 1 || body.OlderThanDays > 3650 {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	result, err := s.uc.PruneArtifacts.Execute(ctx, time.Duration(body.OlderThanDays)*24*time.Hour)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleUIAdminRemoveWorkerJSON deletes an offline worker.
func (s *Server) handleUIAdminRemoveWorkerJSON(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	if err := s.uc.Admin.RemoveWorker(ctx, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
