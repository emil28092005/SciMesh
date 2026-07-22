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
//
//nolint:unused // used by repository methods once phase 2 replaces the stubs
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
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
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return fn(ctx) // already inside a transaction — join it, don't nest
	}

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

// conn returns the transaction bound to ctx, or the pool when there is none.
//
//nolint:unused // every repository method will route through this in phase 2
func conn(ctx context.Context, pool *pgxpool.Pool) querier {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return pool
}
