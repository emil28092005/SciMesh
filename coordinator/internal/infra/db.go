// DB: the PostgreSQL connection pool.
package infra

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool builds the single shared pool. The caller owns its lifetime and must
// Close() it on shutdown.
func NewPool(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	poolCfg.MaxConns = cfg.DBMaxConns

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, err
	}
	// pgxpool.New is lazy, so without this ping a bad DATABASE_URL would only
	// surface on the first request instead of at startup.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
