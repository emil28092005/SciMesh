package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// UIReadRepo contains bounded, deterministic read queries for the operator UI.
type UIReadRepo struct{ db *sql.DB }

func NewUIReadRepo(db *sql.DB) *UIReadRepo { return &UIReadRepo{db: db} }

func (r *UIReadRepo) GetJob(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	return NewJobRepo(r.db).Get(ctx, id)
}

func (r *UIReadRepo) ListTasksByJob(ctx context.Context, jobID uuid.UUID) ([]domain.Task, error) {
	rows, err := conn(ctx, r.db).QueryContext(ctx,
		"SELECT "+taskColumns+" FROM tasks WHERE job_id = ? ORDER BY chunk_index ASC", jobID.String())
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	tasks := make([]domain.Task, 0)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, *task)
	}
	return tasks, rows.Err()
}

func (r *UIReadRepo) ListWorkers(ctx context.Context, limit int) ([]domain.Worker, error) {
	if limit < 1 || limit > 100 {
		return nil, domain.ErrInvalidInput
	}
	rows, err := conn(ctx, r.db).QueryContext(ctx,
		"SELECT "+workerColumns+" FROM workers ORDER BY last_heartbeat_at DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("list workers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	workers := make([]domain.Worker, 0)
	for rows.Next() {
		worker, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		workers = append(workers, *worker)
	}
	return workers, rows.Err()
}

func (r *UIReadRepo) ListArtifactsByJob(ctx context.Context, jobID uuid.UUID) ([]domain.Artifact, error) {
	rows, err := conn(ctx, r.db).QueryContext(ctx,
		"SELECT "+artifactColumns+" FROM artifacts WHERE job_id = ? ORDER BY created_at ASC, id ASC",
		jobID.String())
	if err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	artifacts := make([]domain.Artifact, 0)
	for rows.Next() {
		artifact, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, *artifact)
	}
	return artifacts, rows.Err()
}
