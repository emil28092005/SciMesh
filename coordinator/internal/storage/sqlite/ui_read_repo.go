package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// UIReadRepo contains bounded, deterministic read queries for the operator UI.
type UIReadRepo struct{ db *sql.DB }

func NewUIReadRepo(db *sql.DB) *UIReadRepo { return &UIReadRepo{db: db} }

func (r *UIReadRepo) GetJob(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	return NewJobRepo(r.db).Get(ctx, id)
}

func (r *UIReadRepo) ListJobs(ctx context.Context, owner *uuid.UUID, limit int) ([]domain.Job, error) {
	if limit < 1 || limit > 100 {
		return nil, domain.ErrInvalidInput
	}
	query := "SELECT " + jobColumns + " FROM jobs"
	args := []any{}
	if owner != nil {
		query += " WHERE owner_id = ?"
		args = append(args, owner.String())
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := conn(ctx, r.db).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	jobs := make([]domain.Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	return jobs, rows.Err()
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

func (r *UIReadRepo) ListTasksByJobs(ctx context.Context, jobIDs []uuid.UUID) (map[uuid.UUID][]domain.Task, error) {
	out := make(map[uuid.UUID][]domain.Task, len(jobIDs))
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
		"SELECT "+taskColumns+" FROM tasks WHERE job_id IN ("+strings.Join(placeholders, ", ")+") ORDER BY job_id ASC, chunk_index ASC",
		args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks by jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out[task.JobID] = append(out[task.JobID], *task)
	}
	return out, rows.Err()
}

func (r *UIReadRepo) ListWorkers(ctx context.Context, limit int) ([]domain.Worker, error) {
	return r.listWorkers(ctx, "", nil, limit)
}

func (r *UIReadRepo) ListWorkersByOwner(ctx context.Context, owner uuid.UUID, limit int) ([]domain.Worker, error) {
	return r.listWorkers(ctx, " WHERE owner_id = ?", []any{owner.String()}, limit)
}

func (r *UIReadRepo) listWorkers(ctx context.Context, clause string, args []any, limit int) ([]domain.Worker, error) {
	if limit < 1 || limit > 100 {
		return nil, domain.ErrInvalidInput
	}
	query := "SELECT " + workerColumns + " FROM workers" + clause +
		" ORDER BY last_heartbeat_at DESC, id DESC LIMIT ?"
	fullArgs := append(args, limit)
	rows, err := conn(ctx, r.db).QueryContext(ctx, query, fullArgs...)
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
