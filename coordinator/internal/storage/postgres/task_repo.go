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

// TaskRepo implements usecase.TaskRepository.
type TaskRepo struct {
	pool *pgxpool.Pool
}

func NewTaskRepo(pool *pgxpool.Pool) *TaskRepo {
	return &TaskRepo{pool: pool}
}

var _ usecase.TaskRepository = (*TaskRepo)(nil)

// taskColumns is the single source of truth for the shape scanTask expects.
// Every query that returns a task selects exactly this list, in this order —
// three hand-written column lists would drift apart within a week.
const taskColumns = `id, job_id, chunk_index, workload, input_uri, input_sha256,
	parameters, status, attempt, max_attempts, lease_owner, lease_expires_at,
	result_uri, result_sha256, metrics, error_code, error_message,
	created_at, started_at, completed_at, version`

// scanTask maps one row onto an entity.
//
// status is read into a plain string rather than domain.TaskStatus: pgx does
// not know the task_status enum, and going through string keeps the driver out
// of the domain's type system.
func scanTask(row pgx.Row) (*domain.Task, error) {
	var (
		t      domain.Task
		status string
	)
	err := row.Scan(
		&t.ID, &t.JobID, &t.ChunkIndex, &t.Workload, &t.InputURI, &t.InputSHA256,
		&t.Parameters, &status, &t.Attempt, &t.MaxAttempts, &t.LeaseOwner, &t.LeaseExpiresAt,
		&t.ResultURI, &t.ResultSHA256, &t.Metrics, &t.ErrorCode, &t.ErrorMessage,
		&t.CreatedAt, &t.StartedAt, &t.CompletedAt, &t.Version,
	)
	if err != nil {
		return nil, err
	}
	t.Status = domain.TaskStatus(status)
	return &t, nil
}

// claimNextSQL leases one task in a single statement.
//
// FOR UPDATE SKIP LOCKED is what makes concurrent coordinators safe: each
// process locks a different candidate row instead of queueing on the same one,
// so no task is ever handed to two workers and no claim blocks behind another.
// Splitting this into SELECT + UPDATE would reintroduce exactly that race.
//
// The CTE column is aliased to cid so the RETURNING list below can use bare
// column names without colliding with the candidate relation.
const claimNextSQL = `
WITH candidate AS (
    SELECT id AS cid
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
WHERE tasks.id = candidate.cid
RETURNING ` + taskColumns

// ClaimNext atomically leases the next eligible task.
func (r *TaskRepo) ClaimNext(ctx context.Context, f usecase.ClaimFilter) (*domain.Task, error) {
	workloads := f.Workloads
	if workloads == nil {
		workloads = []string{} // NULL would make the cardinality() guard fail
	}

	var task *domain.Task
	err := withRetry(ctx, func(ctx context.Context) error {
		row := conn(ctx, r.pool).QueryRow(ctx, claimNextSQL, workloads, f.Owner, f.LeaseUntil, f.Now)
		t, err := scanTask(row)
		if errors.Is(err, pgx.ErrNoRows) {
			task = nil
			return nil // an empty queue is a normal state, not a failure
		}
		if err != nil {
			return err
		}
		task = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	return task, nil
}

const getForUpdateSQL = `SELECT ` + taskColumns + ` FROM tasks WHERE id = $1 FOR UPDATE`

// GetForUpdate reads a task and holds its row lock until the caller's
// transaction ends, so read-modify-write use cases cannot interleave.
func (r *TaskRepo) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	t, err := scanTask(conn(ctx, r.pool).QueryRow(ctx, getForUpdateSQL, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// updateTaskSQL writes the mutated entity back under optimistic concurrency.
//
// The entity has already incremented its Version in memory, so the new value
// goes into SET while the guard in WHERE compares against the previous one.
const updateTaskSQL = `
UPDATE tasks
SET status           = $2,
    attempt          = $3,
    lease_owner      = $4,
    lease_expires_at = $5,
    result_uri       = $6,
    result_sha256    = $7,
    metrics          = $8,
    error_code       = $9,
    error_message    = $10,
    started_at       = $11,
    completed_at     = $12,
    version          = $13
WHERE id = $1 AND version = $13 - 1`

func (r *TaskRepo) Update(ctx context.Context, t *domain.Task) error {
	tag, err := conn(ctx, r.pool).Exec(ctx, updateTaskSQL,
		t.ID, string(t.Status), t.Attempt, t.LeaseOwner, t.LeaseExpiresAt,
		t.ResultURI, t.ResultSHA256, t.Metrics, t.ErrorCode, t.ErrorMessage,
		t.StartedAt, t.CompletedAt, t.Version,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Either the row vanished or someone else advanced its version while we
		// held a stale copy. Both mean this write must not land.
		return domain.ErrLeaseConflict
	}
	return nil
}

const insertTaskSQL = `
INSERT INTO tasks (id, job_id, chunk_index, workload, input_uri, input_sha256,
                   parameters, status, attempt, max_attempts, created_at, version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

// InsertBatch writes every task in one round trip. It runs inside the caller's
// transaction, which is what makes "all tasks or none" hold.
func (r *TaskRepo) InsertBatch(ctx context.Context, tasks []*domain.Task) error {
	if len(tasks) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, t := range tasks {
		batch.Queue(insertTaskSQL,
			t.ID, t.JobID, t.ChunkIndex, t.Workload, t.InputURI, t.InputSHA256,
			jsonbOrEmpty(t.Parameters), string(t.Status), t.Attempt, t.MaxAttempts, t.CreatedAt, t.Version,
		)
	}

	results := conn(ctx, r.pool).SendBatch(ctx, batch)
	for range tasks {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return err
		}
	}
	return results.Close()
}

const listCompletedSQL = `
SELECT ` + taskColumns + `
FROM tasks
WHERE job_id = $1 AND status = 'completed'
ORDER BY chunk_index`

// ListCompleted returns results in chunk order, which the stitcher relies on:
// a non-deterministic order would make the merged output depend on which worker
// happened to finish first.
func (r *TaskRepo) ListCompleted(ctx context.Context, jobID uuid.UUID) ([]*domain.Task, error) {
	rows, err := conn(ctx, r.pool).Query(ctx, listCompletedSQL, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

const countByStatusSQL = `SELECT status, count(*) FROM tasks WHERE job_id = $1 GROUP BY status`

func (r *TaskRepo) CountByStatus(ctx context.Context, jobID uuid.UUID) (map[domain.TaskStatus]int, error) {
	rows, err := conn(ctx, r.pool).Query(ctx, countByStatusSQL, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[domain.TaskStatus]int)
	for rows.Next() {
		var (
			status string
			n      int
		)
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		counts[domain.TaskStatus(status)] = n
	}
	return counts, rows.Err()
}

// expireLeasesSQL applies the lease-expiry rule set-based, mirroring
// domain.Task.ExpireLease: requeue while attempts remain, otherwise fail.
//
// It is one statement rather than a load-decide-save loop because several
// coordinators run it concurrently; an atomic UPDATE makes the duplicate work
// harmless — the loser simply updates zero rows.
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
WHERE status = 'leased' AND lease_expires_at < $1`

func (r *TaskRepo) ExpireLeases(ctx context.Context, now time.Time) (int64, error) {
	var affected int64
	err := withRetry(ctx, func(ctx context.Context) error {
		tag, err := conn(ctx, r.pool).Exec(ctx, expireLeasesSQL, now, domain.ErrCodeLeaseExpired)
		if err != nil {
			return err
		}
		affected = tag.RowsAffected()
		return nil
	})
	return affected, err
}
