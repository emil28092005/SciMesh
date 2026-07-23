package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
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
var taskColumns = []string{
	"id", "job_id", "chunk_index", "workload", "input_uri", "input_artifact_id", "input_sha256",
	"parameters", "status", "attempt", "max_attempts", "lease_owner", "lease_expires_at",
	"result_artifact_id", "metrics", "error_code", "error_message",
	"created_at", "started_at", "completed_at", "version",
}

// taskColumnList is the same set as a comma string, for the raw claim query's
// RETURNING clause, which the builder does not touch.
var taskColumnList = strings.Join(taskColumns, ", ")

// scanTask maps one row onto an entity.
//
// status is read into a plain string rather than domain.TaskStatus: pgx does
// not know the task_status enum, and going through string keeps the driver out
// of the domain's type system.
func scanTask(row pgx.Row) (*domain.Task, error) {
	var (
		t      domain.Task
		status string
		// input_uri is nullable now (uploaded shards have none), so it cannot
		// scan straight into a string; NULL becomes the empty InputURI.
		inputURI *string
	)
	err := row.Scan(
		&t.ID, &t.JobID, &t.ChunkIndex, &t.Workload, &inputURI, &t.InputArtifactID, &t.InputSHA256,
		&t.Parameters, &status, &t.Attempt, &t.MaxAttempts, &t.LeaseOwner, &t.LeaseExpiresAt,
		&t.ResultArtifactID, &t.Metrics, &t.ErrorCode, &t.ErrorMessage,
		&t.CreatedAt, &t.StartedAt, &t.CompletedAt, &t.Version,
	)
	if err != nil {
		return nil, err
	}
	if inputURI != nil {
		t.InputURI = *inputURI
	}
	t.Status = domain.TaskStatus(status)
	return &t, nil
}

// claimNextSQL leases one task in a single statement.
//
// Left as raw SQL on purpose: it is a data-modifying CTE with FOR UPDATE SKIP
// LOCKED, which no query builder expresses — and which is the whole point.
// SKIP LOCKED is what makes concurrent coordinators safe: each process locks a
// different candidate row instead of queueing on the same one, so no task is
// ever handed to two workers and no claim blocks behind another. Splitting this
// into SELECT + UPDATE would reintroduce exactly that race.
var claimNextSQL = `
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
RETURNING ` + taskColumnList

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

// Get reads a task without locking its row.
func (r *TaskRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	sql, args, err := psql.Select(taskColumns...).
		From("tasks").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return nil, err
	}
	t, err := scanTask(conn(ctx, r.pool).QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// GetForUpdate reads a task and holds its row lock until the caller's
// transaction ends, so read-modify-write use cases cannot interleave.
func (r *TaskRepo) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	sql, args, err := psql.Select(taskColumns...).
		From("tasks").
		Where(sq.Eq{"id": id}).
		Suffix("FOR UPDATE").
		ToSql()
	if err != nil {
		return nil, err
	}
	t, err := scanTask(conn(ctx, r.pool).QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// Update writes the mutated entity back under optimistic concurrency. The entity
// has already incremented its Version in memory, so the new value goes into SET
// while the WHERE guard matches against the previous one (Version-1).
func (r *TaskRepo) Update(ctx context.Context, t *domain.Task) error {
	sql, args, err := psql.Update("tasks").
		SetMap(map[string]any{
			"status":             string(t.Status),
			"attempt":            t.Attempt,
			"lease_owner":        t.LeaseOwner,
			"lease_expires_at":   t.LeaseExpiresAt,
			"result_artifact_id": t.ResultArtifactID,
			"metrics":            t.Metrics,
			"error_code":         t.ErrorCode,
			"error_message":      t.ErrorMessage,
			"started_at":         t.StartedAt,
			"completed_at":       t.CompletedAt,
			"version":            t.Version,
		}).
		Where(sq.Eq{"id": t.ID, "version": t.Version - 1}).
		ToSql()
	if err != nil {
		return err
	}

	tag, err := conn(ctx, r.pool).Exec(ctx, sql, args...)
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

// InsertBatch writes every task in one round trip. It runs inside the caller's
// transaction, which is what makes "all tasks or none" hold.
func (r *TaskRepo) InsertBatch(ctx context.Context, tasks []*domain.Task) error {
	if len(tasks) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, t := range tasks {
		sql, args, err := psql.Insert("tasks").
			Columns("id", "job_id", "chunk_index", "workload", "input_uri", "input_artifact_id",
				"input_sha256", "parameters", "status", "attempt", "max_attempts", "created_at", "version").
			// input_uri is stored NULL (not "") when empty, so the ck_tasks_has_input
			// check actually bites: a task with neither a URI nor an artifact fails.
			Values(t.ID, t.JobID, t.ChunkIndex, t.Workload, nullIfEmpty(t.InputURI), t.InputArtifactID,
				t.InputSHA256, jsonbOrEmpty(t.Parameters), string(t.Status), t.Attempt, t.MaxAttempts, t.CreatedAt, t.Version).
			ToSql()
		if err != nil {
			return err
		}
		batch.Queue(sql, args...)
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

// ListCompleted returns results in chunk order, which the stitcher relies on:
// a non-deterministic order would make the merged output depend on which worker
// happened to finish first.
func (r *TaskRepo) ListCompleted(ctx context.Context, jobID uuid.UUID) ([]*domain.Task, error) {
	sql, args, err := psql.Select(taskColumns...).
		From("tasks").
		Where(sq.Eq{"job_id": jobID, "status": "completed"}).
		OrderBy("chunk_index").
		ToSql()
	if err != nil {
		return nil, err
	}

	rows, err := conn(ctx, r.pool).Query(ctx, sql, args...)
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

func (r *TaskRepo) CountByStatus(ctx context.Context, jobID uuid.UUID) (map[domain.TaskStatus]int, error) {
	sql, args, err := psql.Select("status", "count(*)").
		From("tasks").
		Where(sq.Eq{"job_id": jobID}).
		GroupBy("status").
		ToSql()
	if err != nil {
		return nil, err
	}

	rows, err := conn(ctx, r.pool).Query(ctx, sql, args...)
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
// Left as raw SQL: the branching lives in CASE expressions inside the SET, which
// a builder cannot express more clearly than this. It is one statement rather
// than a load-decide-save loop because several coordinators run it concurrently;
// an atomic UPDATE makes the duplicate work harmless — the loser updates zero rows.
var expireLeasesSQL = `
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
