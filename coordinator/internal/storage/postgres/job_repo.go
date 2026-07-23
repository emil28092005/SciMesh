package postgres

import (
	"context"
	"errors"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// JobRepo implements usecase.JobRepository.
type JobRepo struct {
	pool *pgxpool.Pool
}

func NewJobRepo(pool *pgxpool.Pool) *JobRepo {
	return &JobRepo{pool: pool}
}

var _ usecase.JobRepository = (*JobRepo)(nil)

var jobColumns = []string{"id", "workload", "input_uri", "parameters", "status", "created_at", "completed_at"}

// Insert runs inside the caller's transaction, alongside the job's tasks — that
// is what makes "all tasks or none" hold.
func (r *JobRepo) Insert(ctx context.Context, j *domain.Job) error {
	sql, args, err := psql.Insert("jobs").
		Columns("id", "workload", "input_uri", "parameters", "status", "created_at").
		Values(j.ID, j.Workload, j.InputURI, jsonbOrEmpty(j.Parameters), string(j.Status), j.CreatedAt).
		ToSql()
	if err != nil {
		return err
	}
	_, err = conn(ctx, r.pool).Exec(ctx, sql, args...)
	return err
}

func (r *JobRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	sql, args, err := psql.Select(jobColumns...).
		From("jobs").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return nil, err
	}

	var (
		j      domain.Job
		status string
	)
	err = conn(ctx, r.pool).QueryRow(ctx, sql, args...).Scan(
		&j.ID, &j.Workload, &j.InputURI, &j.Parameters, &status, &j.CreatedAt, &j.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	j.Status = domain.JobStatus(status)
	return &j, nil
}

func (r *JobRepo) UpdateStatus(ctx context.Context, id uuid.UUID,
	status domain.JobStatus, completedAt *time.Time) error {

	sql, args, err := psql.Update("jobs").
		SetMap(map[string]any{
			"status":       string(status),
			"completed_at": completedAt,
		}).
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return err
	}

	tag, err := conn(ctx, r.pool).Exec(ctx, sql, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrJobNotFound
	}
	return nil
}
