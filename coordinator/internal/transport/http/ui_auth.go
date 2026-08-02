package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/authctx"
	tokenpkg "github.com/emil28092005/SciMesh/coordinator/internal/token"
)

// sessionCookie holds the userservice JWT for the operator UI. It is httpOnly so
// page scripts cannot read the token, and scoped to /ui so it never rides along
// with worker API calls.
const sessionCookie = "scimesh_session"

// withUISession gates the operator UI on a valid userservice session cookie.
// A missing or invalid token redirects to the login page rather than returning
// 401, because the caller here is a browser, not an API client. On success it
// stamps the requester so downstream handlers can scope views by owner.
func withUISession(v tokenVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(sessionCookie)
			if err != nil || c.Value == "" {
				redirectToLogin(w, r)
				return
			}
			claims, err := v.Verify(c.Value)
			if err != nil {
				// Expired or tampered: drop the stale cookie and re-authenticate.
				clearSessionCookie(w, r)
				redirectToLogin(w, r)
				return
			}
			ctx := authctx.With(r.Context(), authctx.Requester{
				UserID:   claims.UserID,
				Role:     claims.Role,
				Verified: claims.Verified,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// tokenVerifier is satisfied by *token.Verifier; taking an interface keeps the
// UI auth testable with a stub.
type tokenVerifier interface {
	Verify(raw string) (tokenpkg.Claims, error)
}

func (s *Server) handleUILoginForm(w http.ResponseWriter, r *http.Request) {
	s.renderUI(w, "login.html", map[string]any{"Error": r.URL.Query().Get("error"), "Next": r.URL.Query().Get("next")})
}

func (s *Server) handleUIRegisterForm(w http.ResponseWriter, r *http.Request) {
	s.renderUI(w, "register.html", map[string]any{"Error": r.URL.Query().Get("error")})
}

// handleUILogin exchanges the submitted credentials for a userservice token and
// stores it in the session cookie. The coordinator never sees or stores the
// password beyond forwarding it once.
func (s *Server) handleUILogin(w http.ResponseWriter, r *http.Request) {
	email, password := r.FormValue("email"), r.FormValue("password")

	status, body, err := s.callUserservice(r.Context(), "/login", email, password)
	if err != nil {
		s.log.Error("userservice login call", "err", err)
		http.Redirect(w, r, "/ui/login?error=service+unavailable", http.StatusSeeOther)
		return
	}
	if status != http.StatusOK {
		http.Redirect(w, r, "/ui/login?error=invalid+email+or+password", http.StatusSeeOther)
		return
	}

	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Token == "" {
		http.Redirect(w, r, "/ui/login?error=service+unavailable", http.StatusSeeOther)
		return
	}
	setSessionCookie(w, r, resp.Token)
	// Land back where the user was headed (e.g. /ui/admin); never follow a
	// value that escapes the UI prefix — that would be an open redirect.
	next := strings.TrimSpace(r.FormValue("next"))
	if next == "" || !strings.HasPrefix(next, "/ui/") {
		next = "/ui"
	}
	//nolint:gosec // G710: next is validated to start with /ui/ just above
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// handleUIRegister creates an account through the userservice, then sends the
// user to the login page. The new account is a plain user until an admin
// promotes or verifies it.
func (s *Server) handleUIRegister(w http.ResponseWriter, r *http.Request) {
	email, password := r.FormValue("email"), r.FormValue("password")

	status, _, err := s.callUserservice(r.Context(), "/register", email, password)
	if err != nil {
		s.log.Error("userservice register call", "err", err)
		http.Redirect(w, r, "/ui/register?error=service+unavailable", http.StatusSeeOther)
		return
	}
	switch status {
	case http.StatusCreated:
		http.Redirect(w, r, "/ui/login?error=registered,+please+log+in", http.StatusSeeOther)
	case http.StatusConflict:
		http.Redirect(w, r, "/ui/register?error=email+already+registered", http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/ui/register?error=invalid+email+or+password", http.StatusSeeOther)
	}
}

func (s *Server) handleUILogout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w, r)
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}

// callUserservice POSTs credentials to the userservice and returns its status
// and body. It is the only runtime dependency on the userservice — login and
// registration; token verification stays local.
func (s *Server) callUserservice(ctx context.Context, path, email, password string) (int, []byte, error) {
	payload, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.userserviceURL+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Cap the response; login/register bodies are tiny.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	// Secure is set under TLS; a local demo runs plain HTTP, where forcing
	// Secure would stop the browser from ever sending the cookie back.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure follows r.TLS by design
		Name:     sessionCookie,
		Value:    token,
		Path:     "/ui",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(24 * time.Hour),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure follows r.TLS by design
		Name:     sessionCookie,
		Value:    "",
		Path:     "/ui",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	// Remember where the user was headed so a successful login lands back
	// there (e.g. /ui/admin) instead of the control room.
	next := r.URL.Path
	if !strings.HasPrefix(next, "/ui/") {
		next = ""
	}
	target := "/ui/login"
	if next != "" {
		target += "?next=" + url.QueryEscape(next)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
