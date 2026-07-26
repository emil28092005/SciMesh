package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TaskResultRepo records and tallies quorum votes for untrusted task results.
type TaskResultRepo struct {
	pool *pgxpool.Pool
}

func NewTaskResultRepo(pool *pgxpool.Pool) *TaskResultRepo {
	return &TaskResultRepo{pool: pool}
}

// RecordVote stores (or replaces) one owner's vote for a task's result.
func (r *TaskResultRepo) RecordVote(ctx context.Context, taskID, ownerID uuid.UUID, sha256 string, artifactID uuid.UUID) error {
	const sql = `
INSERT INTO task_results (task_id, owner_id, result_sha256, result_artifact_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (task_id, owner_id) DO UPDATE
SET result_sha256      = EXCLUDED.result_sha256,
    result_artifact_id = EXCLUDED.result_artifact_id,
    created_at         = now()`
	if _, err := conn(ctx, r.pool).Exec(ctx, sql, taskID, ownerID, sha256, artifactID); err != nil {
		return fmt.Errorf("record vote: %w", err)
	}
	return nil
}

// CountAgreeing returns how many distinct owners have voted for the given result
// hash on this task — the size of the agreeing set the quorum is measured
// against.
func (r *TaskResultRepo) CountAgreeing(ctx context.Context, taskID uuid.UUID, sha256 string) (int, error) {
	const sql = `SELECT count(DISTINCT owner_id) FROM task_results WHERE task_id = $1 AND result_sha256 = $2`
	var n int
	if err := conn(ctx, r.pool).QueryRow(ctx, sql, taskID, sha256).Scan(&n); err != nil {
		return 0, fmt.Errorf("count agreeing: %w", err)
	}
	return n, nil
}
