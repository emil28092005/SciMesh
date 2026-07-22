package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// TaskRepo implements usecase.TaskRepository.
type TaskRepo struct {
	pool *pgxpool.Pool
}

func NewTaskRepo(pool *pgxpool.Pool) *TaskRepo {
	return &TaskRepo{pool: pool}
}

var _ usecase.TaskRepository = (*TaskRepo)(nil)

// claimNextSQL leases one task in a single statement.
//
// FOR UPDATE SKIP LOCKED is what makes concurrent coordinators safe: each
// process locks a different candidate row instead of queueing on the same one,
// so no task is ever handed to two workers and no claim blocks behind another.
// Splitting this into SELECT + UPDATE would reintroduce exactly that race.
//
//nolint:unused // wired up in phase 2; kept beside the repository it belongs to
const claimNextSQL = `
WITH candidate AS (
    SELECT id
    FROM tasks
    WHERE status = 'pending'
      AND attempt < max_attempts
      AND (cardinality($1::text[]) = 0 OR workload = ANY($1))
    ORDER BY created_at, chunk_index
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE tasks
SET status           = 'leased',
    attempt          = attempt + 1,
    lease_owner      = $2,
    lease_expires_at = $3,
    started_at       = COALESCE(started_at, $4),
    version          = version + 1
FROM candidate
WHERE tasks.id = candidate.id
RETURNING tasks.id, tasks.job_id, tasks.chunk_index, tasks.workload,
          tasks.input_uri, tasks.input_sha256, tasks.parameters,
          tasks.status, tasks.attempt, tasks.max_attempts,
          tasks.lease_owner, tasks.lease_expires_at, tasks.version;
`

// expireLeasesSQL applies the lease-expiry rule set-based, mirroring
// domain.Task.ExpireLease: requeue while attempts remain, otherwise fail.
//
//nolint:unused // wired up in phase 5; mirrors domain.Task.ExpireLease
const expireLeasesSQL = `
UPDATE tasks
SET status           = CASE WHEN attempt < max_attempts THEN 'pending'::task_status
                            ELSE 'failed'::task_status END,
    lease_owner      = NULL,
    lease_expires_at = NULL,
    error_code       = CASE WHEN attempt >= max_attempts THEN $2 ELSE error_code END,
    error_message    = CASE WHEN attempt >= max_attempts
                            THEN 'lease expired after the final attempt'
                            ELSE error_message END,
    completed_at     = CASE WHEN attempt >= max_attempts THEN $1 ELSE completed_at END,
    version          = version + 1
WHERE status = 'leased' AND lease_expires_at < $1;
`

// TODO(phase 2-6): replace stubs with real pgx queries; the SQL above is ready
// to wire up. Each method maps 1:1 to a roadmap phase.

func (r *TaskRepo) ClaimNext(ctx context.Context, f usecase.ClaimFilter) (*domain.Task, error) {
	// Phase 2: run claimNextSQL; pgx.ErrNoRows -> (nil, nil).
	return nil, usecase.ErrNotImplemented
}

func (r *TaskRepo) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	// Phase 3: SELECT ... WHERE id = $1 FOR UPDATE; no rows -> domain.ErrTaskNotFound.
	return nil, usecase.ErrNotImplemented
}

func (r *TaskRepo) Update(ctx context.Context, t *domain.Task) error {
	// Phase 3: UPDATE ... WHERE id = $1 AND version = $2 (optimistic concurrency).
	return usecase.ErrNotImplemented
}

func (r *TaskRepo) InsertBatch(ctx context.Context, tasks []*domain.Task) error {
	// Phase 2: pgx.Batch or COPY; runs inside the caller's transaction.
	return usecase.ErrNotImplemented
}

func (r *TaskRepo) ListCompleted(ctx context.Context, jobID uuid.UUID) ([]*domain.Task, error) {
	// Phase 6: WHERE job_id = $1 AND status = 'completed' ORDER BY chunk_index.
	return nil, usecase.ErrNotImplemented
}

func (r *TaskRepo) CountByStatus(ctx context.Context, jobID uuid.UUID) (map[domain.TaskStatus]int, error) {
	// Phase 3: SELECT status, count(*) ... GROUP BY status.
	return nil, usecase.ErrNotImplemented
}

func (r *TaskRepo) ExpireLeases(ctx context.Context, now time.Time) (int64, error) {
	// Phase 5: run expireLeasesSQL, return the affected row count.
	return 0, usecase.ErrNotImplemented
}
