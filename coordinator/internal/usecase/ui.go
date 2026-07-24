package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// UIReadRepository is a read-only projection source for the local operator UI.
// It intentionally exposes no storage paths or credentials.
type UIReadRepository interface {
	GetJob(ctx context.Context, jobID uuid.UUID) (*domain.Job, error)
	ListJobs(ctx context.Context, limit int) ([]domain.Job, error)
	ListTasksByJob(ctx context.Context, jobID uuid.UUID) ([]domain.Task, error)
	ListTasksByJobs(ctx context.Context, jobIDs []uuid.UUID) (map[uuid.UUID][]domain.Task, error)
	ListWorkers(ctx context.Context, limit int) ([]domain.Worker, error)
	ListArtifactsByJob(ctx context.Context, jobID uuid.UUID) ([]domain.Artifact, error)
}

type JobCard struct {
	ID        string    `json:"id"`
	Workload  string    `json:"workload"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	Total     int       `json:"total"`
	Pending   int       `json:"pending"`
	Leased    int       `json:"leased"`
	Running   int       `json:"running"`
	Completed int       `json:"completed"`
	Failed    int       `json:"failed"`
	Cancelled int       `json:"cancelled"`
}

type TaskCard struct {
	ID             string     `json:"id"`
	ChunkIndex     int        `json:"chunk_index"`
	Status         string     `json:"status"`
	Attempt        int        `json:"attempt"`
	MaxAttempts    int        `json:"max_attempts"`
	LeaseOwner     string     `json:"lease_owner,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
	ErrorMessage   string     `json:"error_message,omitempty"`
}

type ArtifactCard struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Filename     string `json:"filename"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
	Downloadable bool   `json:"downloadable"`
	Diagnostic   bool   `json:"diagnostic"`
}

type WorkerCard struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Status          string    `json:"status"`
	Capabilities    []string  `json:"capabilities"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
}

type DashboardView struct {
	Jobs    []JobCard
	Workers []WorkerCard
}
type JobDetailView struct {
	JobCard
	Tasks                []TaskCard     `json:"tasks"`
	Artifacts            []ArtifactCard `json:"artifacts"`
	FinalResultAvailable bool           `json:"final_result_available"`
}

type Dashboard struct{ read UIReadRepository }

func NewDashboard(read UIReadRepository) *Dashboard { return &Dashboard{read: read} }

func (d *Dashboard) Overview(ctx context.Context, limit int) (DashboardView, error) {
	jobs, err := d.read.ListJobs(ctx, limit)
	if err != nil {
		return DashboardView{}, err
	}
	workers, err := d.read.ListWorkers(ctx, limit)
	if err != nil {
		return DashboardView{}, err
	}
	out := DashboardView{Jobs: make([]JobCard, 0, len(jobs)), Workers: make([]WorkerCard, 0, len(workers))}
	jobIDs := make([]uuid.UUID, 0, len(jobs))
	for _, job := range jobs {
		jobIDs = append(jobIDs, job.ID)
	}
	tasksByJob, err := d.read.ListTasksByJobs(ctx, jobIDs)
	if err != nil {
		return DashboardView{}, err
	}
	for _, job := range jobs {
		out.Jobs = append(out.Jobs, jobCard(job, tasksByJob[job.ID]))
	}
	for _, worker := range workers {
		out.Workers = append(out.Workers, WorkerCard{ID: worker.ID.String(), Name: worker.Name, Status: string(worker.Status), Capabilities: worker.Capabilities, LastHeartbeatAt: worker.LastHeartbeatAt})
	}
	return out, nil
}

func (d *Dashboard) JobDetail(ctx context.Context, jobID uuid.UUID) (JobDetailView, error) {
	job, err := d.read.GetJob(ctx, jobID)
	if err != nil {
		return JobDetailView{}, err
	}
	tasks, err := d.read.ListTasksByJob(ctx, jobID)
	if err != nil {
		return JobDetailView{}, err
	}
	artifacts, err := d.read.ListArtifactsByJob(ctx, jobID)
	if err != nil {
		return JobDetailView{}, err
	}
	out := JobDetailView{JobCard: jobCard(*job, tasks), Tasks: make([]TaskCard, 0, len(tasks)), Artifacts: make([]ArtifactCard, 0, len(artifacts))}
	for _, task := range tasks {
		card := TaskCard{ID: task.ID.String(), ChunkIndex: task.ChunkIndex, Status: string(task.Status), Attempt: task.Attempt, MaxAttempts: task.MaxAttempts, LeaseExpiresAt: task.LeaseExpiresAt}
		if task.LeaseOwner != nil {
			card.LeaseOwner = *task.LeaseOwner
		}
		if task.ErrorCode != nil {
			card.ErrorCode = *task.ErrorCode
		}
		if task.ErrorMessage != nil {
			card.ErrorMessage = *task.ErrorMessage
		}
		out.Tasks = append(out.Tasks, card)
	}
	for _, artifact := range artifacts {
		diagnostic := artifact.Kind == domain.ArtifactPartialResult
		downloadable := diagnostic || (artifact.Kind == domain.ArtifactFinalResult && out.Status == string(domain.JobCompleted))
		out.Artifacts = append(out.Artifacts, ArtifactCard{ID: artifact.ID.String(), Kind: string(artifact.Kind), Filename: artifact.Filename, SizeBytes: artifact.SizeBytes, SHA256: artifact.SHA256, Downloadable: downloadable, Diagnostic: diagnostic})
		if artifact.Kind == domain.ArtifactFinalResult && downloadable {
			out.FinalResultAvailable = true
		}
	}
	return out, nil
}

func (d *Dashboard) ArtifactBelongsToJob(ctx context.Context, jobID, artifactID uuid.UUID) (bool, error) {
	artifacts, err := d.read.ListArtifactsByJob(ctx, jobID)
	if err != nil {
		return false, err
	}
	for _, a := range artifacts {
		if a.ID == artifactID {
			return true, nil
		}
	}
	return false, nil
}

func jobCard(job domain.Job, tasks []domain.Task) JobCard {
	c := JobCard{ID: job.ID.String(), Workload: job.Workload, CreatedAt: job.CreatedAt}
	for _, task := range tasks {
		c.Total++
		switch task.Status {
		case domain.TaskPending:
			c.Pending++
		case domain.TaskLeased:
			c.Leased++
		case domain.TaskRunning:
			c.Running++
		case domain.TaskCompleted:
			c.Completed++
		case domain.TaskFailed:
			c.Failed++
		case domain.TaskCancelled:
			c.Cancelled++
		}
	}
	p := domain.JobProgress{Job: job, Total: c.Total, Pending: c.Pending, Leased: c.Leased + c.Running, Done: c.Completed, Failed: c.Failed, Cancelled: c.Cancelled}
	c.Status = string(p.DeriveStatus())
	return c
}
