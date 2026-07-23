package postgres

import (
	"context"
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// UIReadRepo contains bounded, deterministic read queries for the operator UI.
type UIReadRepo struct{ pool *pgxpool.Pool }

func NewUIReadRepo(pool *pgxpool.Pool) *UIReadRepo { return &UIReadRepo{pool: pool} }

var _ usecase.UIReadRepository = (*UIReadRepo)(nil)

func (r *UIReadRepo) GetJob(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	job, err := NewJobRepo(r.pool).Get(ctx, id)
	return job, err
}

func (r *UIReadRepo) ListJobs(ctx context.Context, limit int) ([]domain.Job, error) {
	if limit < 1 || limit > 100 {
		return nil, domain.ErrInvalidInput
	}
	sql, args, err := psql.Select(jobColumns...).From("jobs").OrderBy("created_at DESC", "id DESC").Limit(uint64(limit)).ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := conn(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]domain.Job, 0)
	for rows.Next() {
		var j domain.Job
		var status string
		var inputURI *string
		if err := rows.Scan(&j.ID, &j.Workload, &inputURI, &j.Parameters, &status, &j.CreatedAt, &j.CompletedAt); err != nil {
			return nil, err
		}
		if inputURI != nil {
			j.InputURI = *inputURI
		}
		j.Status = domain.JobStatus(status)
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (r *UIReadRepo) ListTasksByJob(ctx context.Context, jobID uuid.UUID) ([]domain.Task, error) {
	sql, args, err := psql.Select(taskColumns...).From("tasks").Where(sq.Eq{"job_id": jobID}).OrderBy("chunk_index ASC").ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := conn(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
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
	sql, args, err := psql.Select(workerColumns...).From("workers").OrderBy("last_heartbeat_at DESC", "id DESC").Limit(uint64(limit)).ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := conn(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list workers: %w", err)
	}
	defer rows.Close()
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
	sql, args, err := psql.Select(artifactColumns...).From("artifacts").Where(sq.Eq{"job_id": jobID}).OrderBy("created_at ASC", "id ASC").ToSql()
	if err != nil {
		return nil, err
	}
	rows, err := conn(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	defer rows.Close()
	artifacts := make([]domain.Artifact, 0)
	for rows.Next() {
		var a domain.Artifact
		var kind string
		if err := rows.Scan(&a.ID, &a.JobID, &a.TaskID, &a.Attempt, &kind, &a.Filename, &a.StorageKey, &a.ContentType, &a.SizeBytes, &a.SHA256, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Kind = domain.ArtifactKind(kind)
		artifacts = append(artifacts, a)
	}
	return artifacts, rows.Err()
}
