package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// TaskRepo implements usecase.TaskRepository on SQLite.
type TaskRepo struct {
	db *sql.DB
}

func NewTaskRepo(db *sql.DB) *TaskRepo {
	return &TaskRepo{db: db}
}

const taskColumns = `id, job_id, chunk_index, workload, input_uri, input_artifact_id, input_sha256,
	parameters, status, attempt, max_attempts, lease_owner, lease_expires_at,
	result_artifact_id, metrics, error_code, error_message,
	created_at, started_at, completed_at, version`

// scanTask maps one row onto a domain.Task.
func scanTask(row interface{ Scan(dest ...any) error }) (*domain.Task, error) {
	var (
		t       domain.Task
		status  string
		params  string
		metrics sql.NullString
	)
	var (
		inputURI, leaseOwner, errorCode, errorMessage sql.NullString
		inputArtifact, resultArtifact                 sql.NullString
		leaseExpiresAt, startedAt, completedAt        sql.NullInt64
		createdAt                                     sql.NullInt64
	)
	if err := row.Scan(
		&t.ID, &t.JobID, &t.ChunkIndex, &t.Workload, &inputURI, &inputArtifact, &t.InputSHA256,
		&params, &status, &t.Attempt, &t.MaxAttempts, &leaseOwner, &leaseExpiresAt,
		&resultArtifact, &metrics, &errorCode, &errorMessage,
		&createdAt, &startedAt, &completedAt, &t.Version,
	); err != nil {
		return nil, err
	}
	if err := decodeJSON(params, &t.Parameters); err != nil {
		return nil, err
	}
	if metrics.Valid && metrics.String != "" {
		if err := decodeJSON(metrics.String, &t.Metrics); err != nil {
			return nil, err
		}
	}
	t.Status = domain.TaskStatus(status)
	t.CreatedAt = decodeTime(createdAt.Int64)
	if inputURI.Valid {
		t.InputURI = inputURI.String
	}
	if leaseOwner.Valid {
		t.LeaseOwner = &leaseOwner.String
	}
	if inputArtifact.Valid {
		if id, err := uuid.Parse(inputArtifact.String); err == nil {
			t.InputArtifactID = &id
		}
	}
	if resultArtifact.Valid {
		if id, err := uuid.Parse(resultArtifact.String); err == nil {
			t.ResultArtifactID = &id
		}
	}
	if leaseExpiresAt.Valid {
		value := decodeTime(leaseExpiresAt.Int64)
		t.LeaseExpiresAt = &value
	}
	if startedAt.Valid {
		value := decodeTime(startedAt.Int64)
		t.StartedAt = &value
	}
	if completedAt.Valid {
		value := decodeTime(completedAt.Int64)
		t.CompletedAt = &value
	}
	if errorCode.Valid {
		t.ErrorCode = &errorCode.String
	}
	if errorMessage.Valid {
		t.ErrorMessage = &errorMessage.String
	}
	return &t, nil
}

// ClaimNext atomically leases the next eligible task. SQLite has no SKIP
// LOCKED: the guarantee comes from the surrounding transaction's write lock —
// the usecase layer always calls ClaimNext inside WithinTx, so SELECT + UPDATE
// cannot interleave with another claimant.
func (r *TaskRepo) ClaimNext(ctx context.Context, f usecase.ClaimFilter) (*domain.Task, error) {
	workloadClause := ""
	workloadArgs := []any{}
	if len(f.Workloads) > 0 {
		placeholders := make([]string, 0, len(f.Workloads))
		for _, w := range f.Workloads {
			placeholders = append(placeholders, "?")
			workloadArgs = append(workloadArgs, w)
		}
		workloadClause = " AND workload IN (" + strings.Join(placeholders, ", ") + ")"
	}
	voterClause := ""
	var voterArg any
	if f.VoterOwner != nil {
		voterClause = " AND NOT EXISTS (SELECT 1 FROM task_results tr WHERE tr.task_id = tasks.id AND tr.owner_id = ?)"
		voterArg = f.VoterOwner.String()
	}
	args := append([]any{f.Owner, f.LeaseUntil.UnixNano(), f.Now.UnixNano()}, workloadArgs...)
	if f.VoterOwner != nil {
		args = append(args, voterArg)
	}
	query := `
UPDATE tasks SET
	status = 'leased',
	attempt = attempt + 1,
	lease_owner = ?,
	lease_expires_at = ?,
	started_at = COALESCE(started_at, ?),
	version = version + 1
WHERE id IN (
	SELECT id FROM tasks
	WHERE status = 'pending' AND attempt < max_attempts` + workloadClause + voterClause + `
	ORDER BY created_at, chunk_index
	LIMIT 1
)
RETURNING ` + taskColumns

	row := conn(ctx, r.db).QueryRowContext(ctx, query, args...)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return task, nil
}

func (r *TaskRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	row := conn(ctx, r.db).QueryRowContext(ctx,
		"SELECT "+taskColumns+" FROM tasks WHERE id = ?", id.String())
	task, err := scanTask(row)
	return task, mapErrNoRows(err, domain.ErrTaskNotFound)
}

// GetForUpdate reads a task. SQLite serializes writers inside a transaction,
// so no row lock is needed: the surrounding write transaction already isolates
// the read-modify-write sequence.
func (r *TaskRepo) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	return r.Get(ctx, id)
}

// Update writes the mutated entity back under optimistic concurrency.
func (r *TaskRepo) Update(ctx context.Context, t *domain.Task) error {
	res, err := conn(ctx, r.db).ExecContext(ctx, `
UPDATE tasks SET
	status = ?, attempt = ?, lease_owner = ?, lease_expires_at = ?,
	result_artifact_id = ?, metrics = ?, error_code = ?, error_message = ?,
	started_at = ?, completed_at = ?, version = ?
WHERE id = ? AND version = ?`,
		string(t.Status), t.Attempt, nullableString(t.LeaseOwner), encodeTimePtr(t.LeaseExpiresAt),
		nullableUUID(t.ResultArtifactID), nullableMetrics(t.Metrics),
		nullableString(t.ErrorCode), nullableString(t.ErrorMessage),
		encodeTimePtr(t.StartedAt), encodeTimePtr(t.CompletedAt), t.Version,
		t.ID.String(), t.Version-1)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrLeaseConflict
	}
	return nil
}

func (r *TaskRepo) InsertBatch(ctx context.Context, tasks []*domain.Task) error {
	for _, t := range tasks {
		_, err := conn(ctx, r.db).ExecContext(ctx, `
INSERT INTO tasks (id, job_id, chunk_index, workload, input_uri, input_artifact_id,
	input_sha256, parameters, status, attempt, max_attempts, created_at, version)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID.String(), t.JobID.String(), t.ChunkIndex, t.Workload,
			nullIfEmpty(t.InputURI), nullableUUID(t.InputArtifactID),
			t.InputSHA256, encodeJSON(t.Parameters), string(t.Status),
			t.Attempt, t.MaxAttempts, encodeTime(t.CreatedAt), t.Version)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *TaskRepo) ListCompleted(ctx context.Context, jobID uuid.UUID) ([]*domain.Task, error) {
	rows, err := conn(ctx, r.db).QueryContext(ctx,
		"SELECT "+taskColumns+" FROM tasks WHERE job_id = ? AND status = ? ORDER BY chunk_index",
		jobID.String(), string(domain.TaskCompleted))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var tasks []*domain.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (r *TaskRepo) CountByStatus(ctx context.Context, jobID uuid.UUID) (map[domain.TaskStatus]int, error) {
	rows, err := conn(ctx, r.db).QueryContext(ctx,
		"SELECT status, count(*) FROM tasks WHERE job_id = ? GROUP BY status", jobID.String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
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

func (r *TaskRepo) CancelByJob(ctx context.Context, jobID uuid.UUID, now time.Time) (int64, error) {
	res, err := conn(ctx, r.db).ExecContext(ctx, `
UPDATE tasks SET
	status = 'cancelled',
	lease_owner = NULL,
	lease_expires_at = NULL,
	error_code = NULL,
	error_message = NULL,
	completed_at = ?,
	version = version + 1
WHERE job_id = ? AND status IN ('pending','leased','running')`,
		now.UnixNano(), jobID.String())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ExpireLeases applies the lease-expiry rule set-based and returns the
// distinct jobs whose aggregate status may have changed.
func (r *TaskRepo) ExpireLeases(ctx context.Context, now time.Time) ([]uuid.UUID, error) {
	rows, err := conn(ctx, r.db).QueryContext(ctx, `
UPDATE tasks SET
	status = CASE WHEN attempt < max_attempts THEN 'pending' ELSE 'failed' END,
	lease_owner = NULL,
	lease_expires_at = NULL,
	error_code = CASE WHEN attempt >= max_attempts THEN ? ELSE error_code END,
	error_message = CASE WHEN attempt >= max_attempts THEN ? ELSE error_message END,
	completed_at = CASE WHEN attempt >= max_attempts THEN ? ELSE completed_at END,
	version = version + 1
WHERE status IN ('leased','running') AND lease_expires_at < ?
RETURNING job_id`,
		domain.ErrCodeLeaseExpired, "lease expired after the final attempt", now.UnixNano(), now.UnixNano())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var affected []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if id, err := uuid.Parse(raw); err == nil && !seen[id] {
			seen[id] = true
			affected = append(affected, id)
		}
	}
	return affected, rows.Err()
}

// nullableString renders a nilable string, or NULL.
func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// nullableMetrics stores nil metrics as NULL, else JSON text.
func nullableMetrics(m map[string]any) any {
	if m == nil {
		return nil
	}
	return encodeJSON(m)
}
