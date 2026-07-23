// Package postgres implements the usecase repository ports on PostgreSQL.
// SQL and pgx types never escape this package.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// querier is satisfied by both *pgxpool.Pool and pgx.Tx, letting every
// repository method run identically inside or outside a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// txKey is an unexported struct type, so no other package can collide with it
// or reach the transaction we stash in the context.
type txKey struct{}

// TxManager implements usecase.TxManager.
type TxManager struct {
	pool *pgxpool.Pool
}

func NewTxManager(pool *pgxpool.Pool) *TxManager {
	return &TxManager{pool: pool}
}

// WithinTx runs fn inside one transaction, committing on success and rolling
// back on any error or panic.
//
// The transaction travels in the context rather than in fn's signature, which
// is what lets the usecase layer express "do these repository calls atomically"
// without its port ever mentioning pgx.
// Retrying happens here, around the whole transaction, and deliberately not
// inside the repositories. Once Postgres aborts a transaction with a
// serialization failure or deadlock, every further statement in it fails too —
// replaying a single query would accomplish nothing. The unit of retry is
// Begin → fn → Commit.
//
// This is safe because fn re-reads its rows (via GetForUpdate) on each attempt,
// so a retry starts from the current state rather than stale entities.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		// Already inside a transaction — join it. Retrying here would be wrong
		// twice over: the outer transaction owns the retry, and re-running fn
		// alone cannot undo what the outer one already wrote.
		return fn(ctx)
	}

	return withRetry(ctx, func(ctx context.Context) error {
		return m.runTx(ctx, fn)
	})
}

func (m *TxManager) runTx(ctx context.Context, fn func(ctx context.Context) error) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	// Rollback after a successful Commit is a no-op, so this defer is safe and
	// also covers the panic path.
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// jsonbOrEmpty keeps a nil map from reaching a NOT NULL jsonb column. pgx
// encodes a nil map as SQL NULL rather than omitting the column, so the
// DEFAULT '{}' never gets a chance to apply.
func jsonbOrEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// nullIfEmpty maps "" to a SQL NULL, so an absent optional string is stored as
// NULL rather than an empty string that would defeat a NOT-NULL-or check.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// conn returns the transaction bound to ctx, or the pool when there is none.
func conn(ctx context.Context, pool *pgxpool.Pool) querier {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return pool
}
