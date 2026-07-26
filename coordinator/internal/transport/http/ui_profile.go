package http

import (
	"encoding/json"
	"net/http"
)

// profileView is the account data shown on the profile page, mirroring the
// userservice /me response.
type profileView struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Verified  bool   `json:"verified"`
	CreatedAt string `json:"created_at"`
}

// handleUIProfile shows the signed-in user's own account. It proxies the
// session token to the userservice /me endpoint, which is the authority on the
// account (email and created_at are not in the JWT).
func (s *Server) handleUIProfile(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		redirectToLogin(w, r)
		return
	}
	status, body, err := s.callUserserviceAuthed(r.Context(), http.MethodGet, "/me", c.Value)
	if err != nil {
		s.log.Error("profile /me proxy", "err", err)
		s.renderUI(w, "profile.html", map[string]any{"Error": "userservice unavailable"})
		return
	}
	if status == http.StatusUnauthorized {
		clearSessionCookie(w, r)
		redirectToLogin(w, r)
		return
	}
	if status != http.StatusOK {
		s.renderUI(w, "profile.html", map[string]any{"Error": "could not load your account"})
		return
	}

	var p profileView
	if err := json.Unmarshal(body, &p); err != nil {
		s.renderUI(w, "profile.html", map[string]any{"Error": "could not read your account"})
		return
	}
	s.renderUI(w, "profile.html", map[string]any{"Profile": p})
}
