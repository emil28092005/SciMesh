// Package http exposes the userservice over HTTP: registration, login, and a
// token-protected /me. It owns routing, request decoding, and error mapping;
// business rules live in the usecase layer.
package http

import (
	"log/slog"
	"net/http"

	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/auth"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/usecase"
)

// UseCases bundles the application services the handlers drive.
type UseCases struct {
	Register             *usecase.Register
	Login                *usecase.Login
	SetVerified          *usecase.SetVerified
	SetRole              *usecase.SetRole
	CreateWorkerKey      *usecase.CreateWorkerKey
	ListWorkerKeys       *usecase.ListWorkerKeys
	ListWorkerKeysAll    *usecase.ListWorkerKeysAll
	RevokeWorkerKey      *usecase.RevokeWorkerKey
	RevokeWorkerKeyAdmin *usecase.RevokeWorkerKeyAdmin
	ExchangeWorkerKey    *usecase.ExchangeWorkerKey
	ListUsers            *usecase.ListUsers
	Users                usecase.UserRepository
}

// NewServer wires the routes and the middleware stack and returns the handler.
// The issuer verifies tokens for the JWT-protected routes.
func NewServer(log *slog.Logger, uc UseCases, issuer auth.Issuer) http.Handler {
	h := &Handlers{
		register:             uc.Register,
		login:                uc.Login,
		setVerified:          uc.SetVerified,
		setRole:              uc.SetRole,
		createWorkerKey:      uc.CreateWorkerKey,
		listWorkerKeys:       uc.ListWorkerKeys,
		listWorkerKeysAll:    uc.ListWorkerKeysAll,
		revokeWorkerKey:      uc.RevokeWorkerKey,
		revokeWorkerKeyAdmin: uc.RevokeWorkerKeyAdmin,
		exchangeWorkerKey:    uc.ExchangeWorkerKey,
		listUsers:            uc.ListUsers,
		users:                uc.Users,
		log:                  log,
	}

	mux := http.NewServeMux()
	// Method-aware patterns (Go 1.22+): a GET to /register is a 405, not a match.
	mux.HandleFunc("GET /health", h.handleHealth)
	mux.HandleFunc("POST /register", h.handleRegister)
	mux.HandleFunc("POST /login", h.handleLogin)
	// /me proves a token round-trips; it sits behind JWT auth.
	mux.Handle("GET /me", chain(http.HandlerFunc(h.handleMe), withJWT(issuer)))

	// Worker keys: a user mints a long-lived key (JWT-protected), and a worker
	// trades it for a short-lived JWT on the public exchange endpoint — the key
	// itself is the credential there, so no prior token is required.
	mux.HandleFunc("POST /worker-tokens/exchange", h.handleExchangeWorkerKey)
	mux.Handle("POST /worker-keys", chain(http.HandlerFunc(h.handleCreateWorkerKey), withJWT(issuer)))
	mux.Handle("GET /worker-keys", chain(http.HandlerFunc(h.handleListWorkerKeys), withJWT(issuer)))
	mux.Handle("DELETE /worker-keys/{id}", chain(http.HandlerFunc(h.handleRevokeWorkerKey), withJWT(issuer)))

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

	// Admin console: lists of every account and every worker key, and the key
	// revoke path the admin console calls (the same DELETE endpoint already
	// lets an admin revoke any key).
	mux.Handle("GET /users", chain(http.HandlerFunc(h.handleListUsers), withJWT(issuer), withAdmin))
	mux.Handle("GET /worker-keys/all", chain(http.HandlerFunc(h.handleListWorkerKeysAll), withJWT(issuer), withAdmin))

	// Outermost first: every request gets an ID and an access-log line.
	return chain(mux, withRequestID, withAccessLog(log))
}
