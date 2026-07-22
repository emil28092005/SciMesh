// Server: the HTTP listener and the background lease reaper, both shut down
// cleanly on a signal.
package infra

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

const shutdownGrace = 15 * time.Second

// Run serves handler until ctx is cancelled, then drains in-flight requests.
func RunServer(ctx context.Context, log *slog.Logger, addr string, handler http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Buffered so this goroutine can exit even when nobody reads the channel
	// (the ctx.Done branch below) — an unbuffered send would leak it forever.
	errCh := make(chan error, 1)
	go func() {
		log.Info("coordinator listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	// A fresh context: ctx is already cancelled, and reusing it would abort the
	// very requests we are trying to let finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// RunReaper periodically reclaims tasks whose lease elapsed, so a worker that
// died without a heartbeat cannot strand its task in 'leased' forever.
func RunReaper(ctx context.Context, log *slog.Logger, uc *usecase.ExpireLeases, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := uc.Execute(ctx)
			if err != nil {
				// Demoted to debug while the repository is still a stub;
				// raise to Warn once phase 5 lands.
				log.Debug("reaper skipped", "err", err)
				continue
			}
			if n > 0 {
				log.Info("reaper requeued expired leases", "count", n)
			}
		}
	}
}
