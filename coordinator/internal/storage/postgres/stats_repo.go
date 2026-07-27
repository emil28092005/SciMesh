package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// Known statuses per entity, so counts are zero-filled and every status is
// always present in the metrics (a flat 0 line beats a gap on the dashboard).
var (
	taskStatuses   = []string{string(domain.TaskPending), string(domain.TaskLeased), string(domain.TaskRunning), string(domain.TaskCompleted), string(domain.TaskFailed), string(domain.TaskCancelled)}
	jobStatuses    = []string{string(domain.JobPending), string(domain.JobRunning), string(domain.JobReducing), string(domain.JobCompleted), string(domain.JobFailed), string(domain.JobCancelled)}
	workerStatuses = []string{string(domain.WorkerOnline), string(domain.WorkerBusy), string(domain.WorkerOffline)}
)

// StatsRepo answers the aggregate status counts the business metrics report. It
// runs one cheap GROUP BY per entity; the collector calls this on every scrape.
type StatsRepo struct {
	pool *pgxpool.Pool
}

func NewStatsRepo(pool *pgxpool.Pool) *StatsRepo {
	return &StatsRepo{pool: pool}
}

// Counts returns status->count maps for tasks, jobs, and workers, each
// zero-filled across its known statuses.
func (r *StatsRepo) Counts(ctx context.Context) (tasks, jobs, workers map[string]int, err error) {
	if tasks, err = r.countByStatus(ctx, "tasks", taskStatuses); err != nil {
		return nil, nil, nil, err
	}
	if jobs, err = r.countByStatus(ctx, "jobs", jobStatuses); err != nil {
		return nil, nil, nil, err
	}
	if workers, err = r.countByStatus(ctx, "workers", workerStatuses); err != nil {
		return nil, nil, nil, err
	}
	return tasks, jobs, workers, nil
}

func (r *StatsRepo) countByStatus(ctx context.Context, table string, known []string) (map[string]int, error) {
	out := make(map[string]int, len(known))
	for _, s := range known {
		out[s] = 0 // zero-fill
	}
	// table is a fixed internal constant, never user input — safe to format.
	rows, err := r.pool.Query(ctx, fmt.Sprintf("SELECT status, count(*) FROM %s GROUP BY status", table))
	if err != nil {
		return nil, fmt.Errorf("count %s by status: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n // an unknown status still shows up, which is a useful signal
	}
	return out, rows.Err()
}
