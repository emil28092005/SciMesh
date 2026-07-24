package memstore

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// UIReadRepo is the in-memory read projection used by HTTP/UI tests.
type UIReadRepo struct {
	jobs      *JobRepo
	tasks     *TaskRepo
	workers   *WorkerRepo
	artifacts *ArtifactRepo
}

func NewUIReadRepo(j *JobRepo, t *TaskRepo, w *WorkerRepo, a *ArtifactRepo) *UIReadRepo {
	return &UIReadRepo{j, t, w, a}
}

var _ usecase.UIReadRepository = (*UIReadRepo)(nil)

func (r *UIReadRepo) GetJob(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	return r.jobs.Get(ctx, id)
}
func (r *UIReadRepo) ListJobs(_ context.Context, limit int) ([]domain.Job, error) {
	if limit < 1 || limit > 100 {
		return nil, domain.ErrInvalidInput
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	out := make([]domain.Job, 0, len(r.jobs.jobs))
	for _, job := range r.jobs.jobs {
		out = append(out, *job)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID.String() > out[j].ID.String()
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (r *UIReadRepo) ListTasksByJob(_ context.Context, jobID uuid.UUID) ([]domain.Task, error) {
	r.tasks.mu.Lock()
	defer r.tasks.mu.Unlock()
	out := []domain.Task{}
	for _, task := range r.tasks.tasks {
		if task.JobID == jobID {
			out = append(out, *clone(task))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChunkIndex < out[j].ChunkIndex })
	return out, nil
}

func (r *UIReadRepo) ListTasksByJobs(ctx context.Context, jobIDs []uuid.UUID) (map[uuid.UUID][]domain.Task, error) {
	out := make(map[uuid.UUID][]domain.Task, len(jobIDs))
	for _, id := range jobIDs {
		tasks, err := r.ListTasksByJob(ctx, id)
		if err != nil {
			return nil, err
		}
		out[id] = tasks
	}
	return out, nil
}
func (r *UIReadRepo) ListWorkers(_ context.Context, limit int) ([]domain.Worker, error) {
	if limit < 1 || limit > 100 {
		return nil, domain.ErrInvalidInput
	}
	r.workers.mu.Lock()
	defer r.workers.mu.Unlock()
	out := []domain.Worker{}
	for _, worker := range r.workers.workers {
		copy := *worker
		copy.Capabilities = append([]string(nil), worker.Capabilities...)
		out = append(out, copy)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastHeartbeatAt.Equal(out[j].LastHeartbeatAt) {
			return out[i].ID.String() > out[j].ID.String()
		}
		return out[i].LastHeartbeatAt.After(out[j].LastHeartbeatAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (r *UIReadRepo) ListArtifactsByJob(_ context.Context, jobID uuid.UUID) ([]domain.Artifact, error) {
	r.artifacts.mu.Lock()
	defer r.artifacts.mu.Unlock()
	out := []domain.Artifact{}
	for _, artifact := range r.artifacts.arts {
		if artifact.JobID == jobID {
			out = append(out, *artifact)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID.String() < out[j].ID.String()
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}
