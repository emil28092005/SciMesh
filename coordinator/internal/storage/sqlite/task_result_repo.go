package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// TaskResultRepo records and tallies quorum votes for untrusted task results.
type TaskResultRepo struct {
	db *sql.DB
}

func NewTaskResultRepo(db *sql.DB) *TaskResultRepo {
	return &TaskResultRepo{db: db}
}

// RecordVote stores (or replaces) one owner's vote for a task's result.
func (r *TaskResultRepo) RecordVote(ctx context.Context, taskID, ownerID uuid.UUID, sha256 string, artifactID uuid.UUID) error {
	_, err := conn(ctx, r.db).ExecContext(ctx, `
INSERT INTO task_results (task_id, owner_id, result_sha256, result_artifact_id, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (task_id, owner_id) DO UPDATE SET
	result_sha256 = excluded.result_sha256,
	result_artifact_id = excluded.result_artifact_id,
	created_at = excluded.created_at`,
		taskID.String(), ownerID.String(), sha256, artifactID.String(), time.Now().UnixNano())
	return err
}

// CountAgreeing returns how many distinct owners have voted for the given
// result hash on this task.
func (r *TaskResultRepo) CountAgreeing(ctx context.Context, taskID uuid.UUID, sha256 string) (int, error) {
	var n int
	err := conn(ctx, r.db).QueryRowContext(ctx,
		"SELECT count(DISTINCT owner_id) FROM task_results WHERE task_id = ? AND result_sha256 = ?",
		taskID.String(), sha256).Scan(&n)
	return n, err
}
