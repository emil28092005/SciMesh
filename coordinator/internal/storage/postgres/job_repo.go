package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
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

// TODO(phase 2-3): replace stubs with real pgx queries.

func (r *JobRepo) Insert(ctx context.Context, j *domain.Job) error {
	// Phase 2: INSERT INTO jobs ...; runs inside the caller's transaction.
	return usecase.ErrNotImplemented
}

func (r *JobRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	// Phase 3: SELECT ... WHERE id = $1; no rows -> domain.ErrJobNotFound.
	return nil, usecase.ErrNotImplemented
}

func (r *JobRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.JobStatus, completedAt *time.Time) error {
	// Phase 3: UPDATE jobs SET status = $2, completed_at = $3 WHERE id = $1.
	return usecase.ErrNotImplemented
}
