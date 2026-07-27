package postgres

import (
	"context"
	"errors"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emil28092005/SciMesh/users/internal/domain"
	"github.com/emil28092005/SciMesh/users/internal/usecase"
)

var workerKeyColumns = []string{
	"id", "user_id", "name", "token_hash", "prefix", "created_at", "last_used_at", "revoked_at",
}

// WorkerKeyRepo implements usecase.WorkerKeyRepository on PostgreSQL.
type WorkerKeyRepo struct {
	pool *pgxpool.Pool
}

func NewWorkerKeyRepo(pool *pgxpool.Pool) *WorkerKeyRepo {
	return &WorkerKeyRepo{pool: pool}
}

func (r *WorkerKeyRepo) Insert(ctx context.Context, k *domain.WorkerKey) error {
	sql, args, err := psql.Insert("worker_keys").
		Columns(workerKeyColumns...).
		Values(k.ID, k.UserID, k.Name, k.TokenHash, k.Prefix, k.CreatedAt, k.LastUsedAt, k.RevokedAt).
		ToSql()
	if err != nil {
		return err
	}
	_, err = conn(ctx, r.pool).Exec(ctx, sql, args...)
	return err
}

func (r *WorkerKeyRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]*domain.WorkerKey, error) {
	sql, args, err := psql.Select(workerKeyColumns...).
		From("worker_keys").
		Where(sq.Eq{"user_id": userID, "revoked_at": nil}).
		OrderBy("created_at DESC").
		ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := conn(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := []*domain.WorkerKey{}
	for rows.Next() {
		k, err := scanWorkerKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (r *WorkerKeyRepo) GetActiveByHash(ctx context.Context, tokenHash string) (*domain.WorkerKey, error) {
	sql, args, err := psql.Select(workerKeyColumns...).
		From("worker_keys").
		Where(sq.Eq{"token_hash": tokenHash, "revoked_at": nil}).
		ToSql()
	if err != nil {
		return nil, err
	}
	return scanWorkerKey(conn(ctx, r.pool).QueryRow(ctx, sql, args...))
}

// Revoke retires a live key the user owns. Scoping the UPDATE to both id and
// user_id means one user can never revoke another's key, and the revoked_at IS
// NULL guard makes a double-revoke a clean 404 rather than a silent success.
func (r *WorkerKeyRepo) Revoke(ctx context.Context, id, userID uuid.UUID) error {
	sql, args, err := psql.Update("worker_keys").
		Set("revoked_at", sq.Expr("now()")).
		Where(sq.Eq{"id": id, "user_id": userID, "revoked_at": nil}).
		ToSql()
	if err != nil {
		return err
	}
	tag, err := conn(ctx, r.pool).Exec(ctx, sql, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return usecase.ErrWorkerKeyNotFound
	}
	return nil
}

func (r *WorkerKeyRepo) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	sql, args, err := psql.Update("worker_keys").
		Set("last_used_at", sq.Expr("now()")).
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return err
	}
	_, err = conn(ctx, r.pool).Exec(ctx, sql, args...)
	return err
}

func scanWorkerKey(row pgx.Row) (*domain.WorkerKey, error) {
	var k domain.WorkerKey
	if err := row.Scan(
		&k.ID, &k.UserID, &k.Name, &k.TokenHash, &k.Prefix,
		&k.CreatedAt, &k.LastUsedAt, &k.RevokedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, usecase.ErrWorkerKeyNotFound
		}
		return nil, err
	}
	return &k, nil
}
