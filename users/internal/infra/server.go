// Server: the HTTP listener, shut down cleanly on a signal.
package infra

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

const shutdownGrace = 15 * time.Second

// RunServer serves handler until ctx is cancelled, then drains in-flight requests.
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
		log.Info("userservice listening", "addr", addr)
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
