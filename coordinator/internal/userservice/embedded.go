// Package userservice is the SciMesh authentication service, embedded into the
// coordinator binary for single-binary deployments. The packages here are the
// same code the standalone `users/` service runs, with its PostgreSQL storage
// replaced by an embedded SQLite backend. The coordinator's HTTP layer talks
// to it through the usual USERSERVICE_URL proxy, so no proxy code changes.
package userservice

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/auth"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/storage/sqlite"
	usershttp "github.com/emil28092005/SciMesh/coordinator/internal/userservice/transport/http"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/usecase"
)

// Config wires the embedded userservice.
type Config struct {
	// DBPath is the sqlite database file (for example <data-dir>/users.db).
	DBPath string
	// JWTSecret must equal the coordinator's JWT_SECRET so tokens verify.
	JWTSecret string
	// AdminEmail/AdminPassword bootstrap the first admin on first run.
	AdminEmail    string
	AdminPassword string
	// Log receives the service's log lines.
	Log *slog.Logger
}

// Serve runs the embedded userservice until ctx is cancelled. It listens only
// on the loopback interface; the coordinator proxies to it internally.
func Serve(ctx context.Context, cfg Config) (string, func() error, error) {
	db, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return "", nil, err
	}
	if err := sqlite.Migrate(ctx, db, cfg.Log); err != nil {
		_ = db.Close()
		return "", nil, err
	}

	clock := NewClock()
	users := sqlite.NewUserRepo(db)
	workerKeys := sqlite.NewWorkerKeyRepo(db)
	hasher := auth.NewHasher(0)
	issuer := auth.NewIssuer(cfg.JWTSecret, 24*time.Hour, clock.Now)

	uc := usershttp.UseCases{
		Register:          usecase.NewRegister(users, hasher, clock),
		Login:             usecase.NewLogin(users, hasher, issuer),
		SetVerified:       usecase.NewSetVerified(users),
		SetRole:           usecase.NewSetRole(users),
		CreateWorkerKey:   usecase.NewCreateWorkerKey(workerKeys, clock),
		ListWorkerKeys:    usecase.NewListWorkerKeys(workerKeys),
		RevokeWorkerKey:   usecase.NewRevokeWorkerKey(workerKeys),
		ExchangeWorkerKey: usecase.NewExchangeWorkerKey(workerKeys, users, issuer, 24*time.Hour),
		Users:             users,
	}

	if cfg.AdminEmail != "" && cfg.AdminPassword != "" {
		created, err := usecase.NewBootstrapAdmin(users, hasher, clock).
			Execute(ctx, cfg.AdminEmail, cfg.AdminPassword)
		if err != nil {
			_ = db.Close()
			return "", nil, fmt.Errorf("bootstrap admin: %w", err)
		}
		if created {
			cfg.Log.Info("embedded userservice created the admin account", "email", cfg.AdminEmail)
		}
	}

	handler := usershttp.NewServer(cfg.Log, uc, issuer)
	handler = http.TimeoutHandler(handler, 15*time.Second, `{"error":"request timeout"}`)

	// Bind an ephemeral loopback port so a second serve instance can never
	// collide with the first; the coordinator's proxy uses the returned
	// address and needs no fixed-port assumption.
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		_ = db.Close()
		return "", nil, fmt.Errorf("listen for embedded userservice: %w", err)
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			cfg.Log.Error("embedded userservice stopped", "err", err)
		}
	}()
	return listener.Addr().String(), func() error { return db.Close() }, nil
}

// NewClock returns the userservice's wall clock.
func NewClock() *Clock { return &Clock{} }

// Clock implements the userservice usecase clock.
type Clock struct{}

// Now returns the current UTC time.
func (c *Clock) Now() time.Time { return time.Now().UTC() }
