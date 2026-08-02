package postgres

import (
	"context"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// AdminReadRepo backs the coordinator admin console: paginated jobs, status
// counters, metrics buckets and storage figures. Read-only.
type AdminReadRepo struct{ pool *pgxpool.Pool }

func NewAdminReadRepo(pool *pgxpool.Pool) *AdminReadRepo { return &AdminReadRepo{pool: pool} }

var _ usecase.AdminReadRepository = (*AdminReadRepo)(nil)

func (r *AdminReadRepo) ListJobsPaginated(ctx context.Context, status string, limit, offset int) ([]domain.Job, int, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, 0, domain.ErrInvalidInput
	}
	countQ := psql.Select("COUNT(*)").From("jobs")
	listQ := psql.Select(jobColumns...).From("jobs")
	if status != "" {
		countQ = countQ.Where(sq.Eq{"status": status})
		listQ = listQ.Where(sq.Eq{"status": status})
	}
	countSQL, args, err := countQ.ToSql()
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := conn(ctx, r.pool).QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count jobs: %w", err)
	}
	listSQL, args, err := listQ.OrderBy("created_at DESC", "id DESC").Limit(uint64(limit)).Offset(uint64(offset)).ToSql()
	if err != nil {
		return nil, 0, err
	}
	rows, err := conn(ctx, r.pool).Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list jobs paginated: %w", err)
	}
	defer rows.Close()
	jobs := make([]domain.Job, 0)
	for rows.Next() {
		var j domain.Job
		var statusRaw string
		if err := rows.Scan(
			&j.ID, &j.Workload, &j.InputURI, &j.Parameters, &statusRaw, &j.CreatedAt, &j.CompletedAt,
			&j.InputArtifactID, &j.ResultArtifactID, &j.ErrorCode, &j.ErrorMessage, &j.ReducerStartedAt,
			&j.OwnerID,
		); err != nil {
			return nil, 0, err
		}
		j.Status = domain.JobStatus(statusRaw)
		jobs = append(jobs, j)
	}
	return jobs, total, rows.Err()
}

func (r *AdminReadRepo) CountJobsByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := conn(ctx, r.pool).Query(ctx, "SELECT status, COUNT(*) FROM jobs GROUP BY status")
	if err != nil {
		return nil, fmt.Errorf("count jobs by status: %w", err)
	}
	defer rows.Close()
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
	sql, args, err := psql.Select("job_id", "status", "COUNT(*)").From("tasks").
		Where(sq.Eq{"job_id": jobIDs}).GroupBy("job_id", "status").ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := conn(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("task counts by jobs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var jobID uuid.UUID
		var status string
		var count int
		if err := rows.Scan(&jobID, &status, &count); err != nil {
			return nil, err
		}
		if out[jobID] == nil {
			out[jobID] = make(map[string]int)
		}
		out[jobID][status] = count
	}
	return out, rows.Err()
}

func (r *AdminReadRepo) JobCountsByDay(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := conn(ctx, r.pool).Query(ctx,
		"SELECT to_char(date_trunc('day', created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day, COUNT(*) FROM jobs WHERE created_at >= $1 GROUP BY 1",
		since.UTC())
	if err != nil {
		return nil, fmt.Errorf("job counts by day: %w", err)
	}
	defer rows.Close()
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
	rows, err := conn(ctx, r.pool).Query(ctx, "SELECT workload, COUNT(*) FROM jobs GROUP BY workload")
	if err != nil {
		return nil, fmt.Errorf("job counts by workload: %w", err)
	}
	defer rows.Close()
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
	var avgSeconds *float64
	err := conn(ctx, r.pool).QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0),
			AVG(CASE WHEN status = 'completed' AND started_at IS NOT NULL
				THEN EXTRACT(EPOCH FROM completed_at - started_at) END)::float8
		FROM tasks`).Scan(&completed, &failed, &avgSeconds)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("task stats: %w", err)
	}
	var avg float64
	if avgSeconds != nil {
		avg = *avgSeconds
	}
	return completed, failed, avg, nil
}

func (r *AdminReadRepo) ArtifactSizeByKind(ctx context.Context) (map[string]int64, error) {
	rows, err := conn(ctx, r.pool).Query(ctx, "SELECT kind, COALESCE(SUM(size_bytes), 0) FROM artifacts GROUP BY kind")
	if err != nil {
		return nil, fmt.Errorf("artifact sizes: %w", err)
	}
	defer rows.Close()
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
	var size int64
	if err := conn(ctx, r.pool).QueryRow(ctx, "SELECT pg_database_size(current_database())").Scan(&size); err != nil {
		return 0, fmt.Errorf("database size: %w", err)
	}
	return size, nil
}
