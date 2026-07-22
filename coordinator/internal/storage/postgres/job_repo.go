package postgres

import (
	"context"
	"errors"
	"time"

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

const jobColumns = `id, workload, input_uri, parameters, status, created_at, completed_at`

const insertJobSQL = `
INSERT INTO jobs (id, workload, input_uri, parameters, status, created_at)
VALUES ($1, $2, $3, $4, $5, $6)`

// Insert runs inside the caller's transaction, alongside the job's tasks — that
// is what makes "all tasks or none" hold.
func (r *JobRepo) Insert(ctx context.Context, j *domain.Job) error {
	_, err := conn(ctx, r.pool).Exec(ctx, insertJobSQL,
		j.ID, j.Workload, j.InputURI, jsonbOrEmpty(j.Parameters), string(j.Status), j.CreatedAt)
	return err
}

const getJobSQL = `SELECT ` + jobColumns + ` FROM jobs WHERE id = $1`

func (r *JobRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	var (
		j      domain.Job
		status string
	)
	err := conn(ctx, r.pool).QueryRow(ctx, getJobSQL, id).Scan(
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

const updateJobStatusSQL = `UPDATE jobs SET status = $2, completed_at = $3 WHERE id = $1`

func (r *JobRepo) UpdateStatus(ctx context.Context, id uuid.UUID,
	status domain.JobStatus, completedAt *time.Time) error {

	tag, err := conn(ctx, r.pool).Exec(ctx, updateJobStatusSQL, id, string(status), completedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrJobNotFound
	}
	return nil
}
