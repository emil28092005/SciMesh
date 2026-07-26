// DB: the PostgreSQL connection pool.
package infra

import (
	"context"
	"log/slog"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool builds the single shared pool. The caller owns its lifetime and must
// Close() it on shutdown.
func NewPool(ctx context.Context, cfg Config, log *slog.Logger) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	poolCfg.MaxConns = cfg.DBMaxConns

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, err
	}
	// pgxpool.New is lazy, so a ping is needed to actually reach the server.
	// It is retried because at startup — especially under docker-compose, where
	// the coordinator can boot before Postgres is accepting connections — a
	// service should wait for its database rather than crash-loop.
	if err := pingWithRetry(ctx, pool, cfg.DBConnectTimeout, log); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// pingWithRetry waits for the database to accept connections, backing off
// between attempts until the budget elapses or ctx is cancelled.
//
// Unlike the transaction retry in storage/postgres, this retries *any* ping
// error: at startup a "connection refused" is the expected, retryable state,
// not an anomaly.
func pingWithRetry(ctx context.Context, pool *pgxpool.Pool, budget time.Duration, log *slog.Logger) error {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 200 * time.Millisecond
	b.MaxInterval = 3 * time.Second
	b.MaxElapsedTime = budget

	attempt := 0
	return backoff.RetryNotify(
		func() error {
			// A bounded per-attempt timeout so one hung dial cannot eat the
			// whole budget in a single try.
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			return pool.Ping(pingCtx)
		},
		backoff.WithContext(b, ctx),
		func(err error, next time.Duration) {
			attempt++
			log.Warn("database not ready, retrying",
				"attempt", attempt, "retry_in", next.String(), "err", err)
		},
	)
}
