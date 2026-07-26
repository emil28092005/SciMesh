// Command userservice runs the SciMesh authentication service: it registers
// users, verifies logins, and issues the HS256 JWTs the coordinator trusts.
package main

import (
	"context"
	"fmt"
	nethttp "net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/emil28092005/SciMesh/users/internal/auth"
	"github.com/emil28092005/SciMesh/users/internal/infra"
	"github.com/emil28092005/SciMesh/users/internal/storage/postgres"
	apihttp "github.com/emil28092005/SciMesh/users/internal/transport/http"
	"github.com/emil28092005/SciMesh/users/internal/usecase"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	// Cancelled on SIGINT/SIGTERM so the HTTP server drains in-flight requests
	// instead of dropping them.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := infra.LoadConfig()
	if err != nil {
		return err
	}

	log, closer, err := infra.NewLogger(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()

	pool, err := infra.NewPool(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Adapters implementing the usecase ports.
	users := postgres.NewUserRepo(pool)
	hasher := auth.NewHasher(cfg.BcryptCost)
	clock := infra.NewClock()
	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.TokenTTL, clock.Now)

	uc := apihttp.UseCases{
		Register: usecase.NewRegister(users, hasher, clock),
		Login:    usecase.NewLogin(users, hasher, issuer),
		Users:    users,
	}

	handler := apihttp.NewServer(log, uc, issuer)
	// A blanket per-request deadline: bcrypt is bounded, so anything slower is a
	// stuck handler we want to shed rather than hold a connection open.
	handler = nethttp.TimeoutHandler(handler, cfg.RequestTimeout, `{"error":"request timeout"}`)

	return infra.RunServer(ctx, log, cfg.Addr, handler)
}
