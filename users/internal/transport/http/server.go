// Package http exposes the userservice over HTTP: registration, login, and a
// token-protected /me. It owns routing, request decoding, and error mapping;
// business rules live in the usecase layer.
package http

import (
	"log/slog"
	"net/http"

	"github.com/emil28092005/SciMesh/users/internal/auth"
	"github.com/emil28092005/SciMesh/users/internal/domain"
	"github.com/emil28092005/SciMesh/users/internal/usecase"
)

// UseCases bundles the application services the handlers drive.
type UseCases struct {
	Register    *usecase.Register
	Login       *usecase.Login
	SetVerified *usecase.SetVerified
	SetRole     *usecase.SetRole
	Users       usecase.UserRepository
}

// NewServer wires the routes and the middleware stack and returns the handler.
// The issuer verifies tokens for the JWT-protected routes.
func NewServer(log *slog.Logger, uc UseCases, issuer auth.Issuer) http.Handler {
	h := &Handlers{
		register:    uc.Register,
		login:       uc.Login,
		setVerified: uc.SetVerified,
		setRole:     uc.SetRole,
		users:       uc.Users,
		log:         log,
	}

	mux := http.NewServeMux()
	// Method-aware patterns (Go 1.22+): a GET to /register is a 405, not a match.
	mux.HandleFunc("GET /health", h.handleHealth)
	mux.HandleFunc("POST /register", h.handleRegister)
	mux.HandleFunc("POST /login", h.handleLogin)
	// /me proves a token round-trips; it sits behind JWT auth.
	mux.Handle("GET /me", chain(http.HandlerFunc(h.handleMe), withJWT(issuer)))

	// Admin-only: grant or revoke the trusted-contributor badge. withAdmin sits
	// inside withJWT so the role is available from the verified token.
	mux.Handle("POST /users/{id}/verify",
		chain(h.handleSetVerified(true), withJWT(issuer), withAdmin))
	mux.Handle("POST /users/{id}/unverify",
		chain(h.handleSetVerified(false), withJWT(issuer), withAdmin))
	mux.Handle("POST /users/{id}/promote",
		chain(h.handleSetRole(domain.RoleAdmin), withJWT(issuer), withAdmin))
	mux.Handle("POST /users/{id}/demote",
		chain(h.handleSetRole(domain.RoleUser), withJWT(issuer), withAdmin))

	// Outermost first: every request gets an ID and an access-log line.
	return chain(mux, withRequestID, withAccessLog(log))
}
