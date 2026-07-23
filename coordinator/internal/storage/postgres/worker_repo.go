package postgres

import (
	"context"
	"errors"
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// WorkerRepo implements usecase.WorkerRepository.
type WorkerRepo struct {
	pool *pgxpool.Pool
}

func NewWorkerRepo(pool *pgxpool.Pool) *WorkerRepo {
	return &WorkerRepo{pool: pool}
}

var workerColumns = []string{"id", "name", "capabilities", "status", "last_heartbeat_at", "created_at", "updated_at"}

func (r *WorkerRepo) Insert(ctx context.Context, w *domain.Worker) error {
	sql, args, err := psql.Insert("workers").
		Columns(workerColumns...).
		// capabilities is a jsonb column; pgx marshals the []string to a JSON array.
		Values(w.ID, w.Name, w.Capabilities, string(w.Status),
			w.LastHeartbeatAt, w.CreatedAt, w.UpdatedAt).
		ToSql()
	if err != nil {
		return err
	}
	if _, err := conn(ctx, r.pool).Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("insert worker: %w", err)
	}
	return nil
}

func (r *WorkerRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Worker, error) {
	sql, args, err := psql.Select(workerColumns...).
		From("workers").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return nil, err
	}

	w, err := scanWorker(conn(ctx, r.pool).QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrWorkerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get worker: %w", err)
	}
	return w, nil
}

func scanWorker(row pgx.Row) (*domain.Worker, error) {
	var (
		w      domain.Worker
		status string
	)
	if err := row.Scan(&w.ID, &w.Name, &w.Capabilities, &status,
		&w.LastHeartbeatAt, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return nil, err
	}
	w.Status = domain.WorkerStatus(status)
	return &w, nil
}
