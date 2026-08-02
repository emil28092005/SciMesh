package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// AdminReadRepo backs the coordinator admin console: paginated jobs, status
// counters, metrics buckets and storage figures. Read-only.
type AdminReadRepo struct{ db *sql.DB }

func NewAdminReadRepo(db *sql.DB) *AdminReadRepo { return &AdminReadRepo{db: db} }

var _ usecase.AdminReadRepository = (*AdminReadRepo)(nil)

func (r *AdminReadRepo) ListJobsPaginated(ctx context.Context, status string, limit, offset int) ([]domain.Job, int, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, 0, domain.ErrInvalidInput
	}
	where := ""
	args := []any{}
	if status != "" {
		where = " WHERE status = ?"
		args = append(args, status)
	}
	var total int
	if err := conn(ctx, r.db).QueryRowContext(ctx, "SELECT COUNT(*) FROM jobs"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count jobs: %w", err)
	}
	rows, err := conn(ctx, r.db).QueryContext(ctx,
		"SELECT "+jobColumns+" FROM jobs"+where+" ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?",
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list jobs paginated: %w", err)
	}
	defer func() { _ = rows.Close() }()
	jobs := make([]domain.Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		jobs = append(jobs, *job)
	}
	return jobs, total, rows.Err()
}

func (r *AdminReadRepo) CountJobsByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := conn(ctx, r.db).QueryContext(ctx, "SELECT status, COUNT(*) FROM jobs GROUP BY status")
	if err != nil {
		return nil, fmt.Errorf("count jobs by status: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = count
	}
	return out, rows.Err()
}

func (r *AdminReadRepo) TaskCountsByJobs(ctx context.Context, jobIDs []uuid.UUID) (map[uuid.UUID]map[string]int, error) {
	out := make(map[uuid.UUID]map[string]int, len(jobIDs))
	if len(jobIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, 0, len(jobIDs))
	args := make([]any, 0, len(jobIDs))
	for _, id := range jobIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id.String())
	}
	rows, err := conn(ctx, r.db).QueryContext(ctx,
		"SELECT job_id, status, COUNT(*) FROM tasks WHERE job_id IN ("+strings.Join(placeholders, ", ")+") GROUP BY job_id, status",
		args...)
	if err != nil {
		return nil, fmt.Errorf("task counts by jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var jobRaw, status string
		var count int
		if err := rows.Scan(&jobRaw, &status, &count); err != nil {
			return nil, err
		}
		jobID, err := uuid.Parse(jobRaw)
		if err != nil {
			return nil, fmt.Errorf("task counts: parse job id: %w", err)
		}
		if out[jobID] == nil {
			out[jobID] = make(map[string]int)
		}
		out[jobID][status] = count
	}
	return out, rows.Err()
}

func (r *AdminReadRepo) JobCountsByDay(ctx context.Context, since time.Time) (map[string]int, error) {
	// created_at is unix nanos; the bucket is the UTC calendar day.
	rows, err := conn(ctx, r.db).QueryContext(ctx,
		"SELECT strftime('%Y-%m-%d', created_at / 1000000000, 'unixepoch') AS day, COUNT(*) FROM jobs WHERE created_at >= ? GROUP BY day",
		since.UTC().UnixNano())
	if err != nil {
		return nil, fmt.Errorf("job counts by day: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int)
	for rows.Next() {
		var day string
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			return nil, err
		}
		out[day] = count
	}
	return out, rows.Err()
}

func (r *AdminReadRepo) JobCountsByWorkload(ctx context.Context) (map[string]int, error) {
	rows, err := conn(ctx, r.db).QueryContext(ctx, "SELECT workload, COUNT(*) FROM jobs GROUP BY workload")
	if err != nil {
		return nil, fmt.Errorf("job counts by workload: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int)
	for rows.Next() {
		var workload string
		var count int
		if err := rows.Scan(&workload, &count); err != nil {
			return nil, err
		}
		out[workload] = count
	}
	return out, rows.Err()
}

func (r *AdminReadRepo) TaskStats(ctx context.Context) (int64, int64, float64, error) {
	var completed, failed int64
	var avgNanos sql.NullFloat64
	err := conn(ctx, r.db).QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0),
			AVG(CASE WHEN status = 'completed' AND started_at IS NOT NULL THEN completed_at - started_at END)
		FROM tasks`).Scan(&completed, &failed, &avgNanos)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("task stats: %w", err)
	}
	return completed, failed, avgNanos.Float64 / 1e9, nil
}

func (r *AdminReadRepo) ArtifactSizeByKind(ctx context.Context) (map[string]int64, error) {
	rows, err := conn(ctx, r.db).QueryContext(ctx, "SELECT kind, COALESCE(SUM(size_bytes), 0) FROM artifacts GROUP BY kind")
	if err != nil {
		return nil, fmt.Errorf("artifact sizes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int64)
	for rows.Next() {
		var kind string
		var size int64
		if err := rows.Scan(&kind, &size); err != nil {
			return nil, err
		}
		out[kind] = size
	}
	return out, rows.Err()
}

func (r *AdminReadRepo) DatabaseSizeBytes(ctx context.Context) (int64, error) {
	var pageCount, pageSize int64
	if err := conn(ctx, r.db).QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return 0, fmt.Errorf("page count: %w", err)
	}
	if err := conn(ctx, r.db).QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, fmt.Errorf("page size: %w", err)
	}
	return pageCount * pageSize, nil
}
