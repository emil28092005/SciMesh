package usecase

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/authctx"
	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/workloads"
)

// UIReadRepository is a read-only projection source for the local operator UI.
// It intentionally exposes no storage paths or credentials.
type UIReadRepository interface {
	GetJob(ctx context.Context, jobID uuid.UUID) (*domain.Job, error)
	// ListJobs returns the most recent jobs. A non-nil owner restricts the list
	// to that user's jobs; nil returns all (operator/admin view).
	ListJobs(ctx context.Context, owner *uuid.UUID, limit int) ([]domain.Job, error)
	ListTasksByJob(ctx context.Context, jobID uuid.UUID) ([]domain.Task, error)
	ListTasksByJobs(ctx context.Context, jobIDs []uuid.UUID) (map[uuid.UUID][]domain.Task, error)
	ListWorkers(ctx context.Context, limit int) ([]domain.Worker, error)
	// ListWorkersByOwner returns the most recent workers registered by one user,
	// for the "my machines" section of the dashboard.
	ListWorkersByOwner(ctx context.Context, owner uuid.UUID, limit int) ([]domain.Worker, error)
	ListArtifactsByJob(ctx context.Context, jobID uuid.UUID) ([]domain.Artifact, error)
}

type JobCard struct {
	ID               string     `json:"id"`
	Workload         string     `json:"workload"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"created_at"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	ReducerStartedAt *time.Time `json:"reducer_started_at,omitempty"`
	ErrorCode        string     `json:"error_code,omitempty"`
	ErrorMessage     string     `json:"error_message,omitempty"`
	Total            int        `json:"total"`
	Pending          int        `json:"pending"`
	Leased           int        `json:"leased"`
	Running          int        `json:"running"`
	Completed        int        `json:"completed"`
	Failed           int        `json:"failed"`
	Cancelled        int        `json:"cancelled"`
}

type TaskCard struct {
	ID             string     `json:"id"`
	ChunkIndex     int        `json:"chunk_index"`
	Status         string     `json:"status"`
	Attempt        int        `json:"attempt"`
	MaxAttempts    int        `json:"max_attempts"`
	LeaseOwner     string     `json:"lease_owner,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
	ErrorMessage   string     `json:"error_message,omitempty"`
}

// ParameterCard is an intentionally small allowlist of run configuration that
// helps an operator verify what is being computed without exposing arbitrary
// job payloads to the browser.
type ParameterCard struct {
	Label string `json:"label"`
	Value string `json:"value"`
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
	Jobs    []JobCard    `json:"jobs"`
	Workers []WorkerCard `json:"workers"`
	// MyWorkers is the signed-in user's own registered workers. Empty for an
	// admin or a basic-auth operator, who instead see the whole fleet in Workers.
	MyWorkers     []WorkerCard `json:"my_workers"`
	ActiveJobs    int          `json:"active_jobs"`
	FinishedJobs  int          `json:"finished_jobs"`
	OnlineWorkers int          `json:"online_workers"`
	// Session is the signed-in user, when the UI runs in session mode. nil under
	// basic auth. Template-only, never serialised to the polling JSON.
	Session *SessionView `json:"-"`
}

// SessionView is the minimal identity the UI header needs to show who is signed
// in and to offer a logout control.
type SessionView struct {
	Role     string
	Verified bool
}

// sessionViewFrom builds the header session info from the request context, or
// nil when the caller is not an authenticated user (basic-auth operator).
func sessionViewFrom(ctx context.Context) *SessionView {
	r, ok := authctx.From(ctx)
	if !ok {
		return nil
	}
	return &SessionView{Role: r.Role, Verified: r.Verified}
}

type JobDetailView struct {
	JobCard
	Tasks                []TaskCard      `json:"tasks"`
	Artifacts            []ArtifactCard  `json:"artifacts"`
	Parameters           []ParameterCard `json:"parameters"`
	FinalResultAvailable bool            `json:"final_result_available"`
	Session              *SessionView    `json:"-"`
}

type Dashboard struct {
	read    UIReadRepository
	catalog *workloads.Catalog
}

func NewDashboard(read UIReadRepository, catalog *workloads.Catalog) *Dashboard {
	return &Dashboard{read: read, catalog: catalog}
}

func (d *Dashboard) Overview(ctx context.Context, limit int) (DashboardView, error) {
	jobs, err := d.read.ListJobs(ctx, uiOwnerFilter(ctx), limit)
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
		card := jobCard(job, tasksByJob[job.ID])
		out.Jobs = append(out.Jobs, card)
		switch card.Status {
		case string(domain.JobCompleted), string(domain.JobFailed), string(domain.JobCancelled):
			out.FinishedJobs++
		default:
			out.ActiveJobs++
		}
	}
	for _, worker := range workers {
		out.Workers = append(out.Workers, workerCard(worker))
		if worker.Status == domain.WorkerOnline || worker.Status == domain.WorkerBusy {
			out.OnlineWorkers++
		}
	}
	// A plain user also gets a dedicated "my machines" list scoped to their own
	// registrations; an admin/operator sees only the fleet above.
	if owner := uiOwnerFilter(ctx); owner != nil {
		mine, err := d.read.ListWorkersByOwner(ctx, *owner, limit)
		if err != nil {
			return DashboardView{}, err
		}
		out.MyWorkers = make([]WorkerCard, 0, len(mine))
		for _, worker := range mine {
			out.MyWorkers = append(out.MyWorkers, workerCard(worker))
		}
	}
	out.Session = sessionViewFrom(ctx)
	return out, nil
}

func workerCard(w domain.Worker) WorkerCard {
	return WorkerCard{
		ID:              w.ID.String(),
		Name:            w.Name,
		Status:          string(w.Status),
		Capabilities:    w.Capabilities,
		LastHeartbeatAt: w.LastHeartbeatAt,
	}
}

func (d *Dashboard) JobDetail(ctx context.Context, jobID uuid.UUID) (JobDetailView, error) {
	job, err := d.read.GetJob(ctx, jobID)
	if err != nil {
		return JobDetailView{}, err
	}
	// A plain user may only open their own job; a mismatch reads as not-found so
	// the page never reveals another user's job exists.
	if err := authorizeJobAccess(ctx, job); err != nil {
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
	workers, err := d.read.ListWorkers(ctx, 100)
	if err != nil {
		return JobDetailView{}, err
	}
	workerNames := make(map[string]string, len(workers))
	for _, worker := range workers {
		workerNames[worker.ID.String()] = worker.Name
	}
	out := JobDetailView{
		JobCard:    jobCard(*job, tasks),
		Tasks:      make([]TaskCard, 0, len(tasks)),
		Artifacts:  make([]ArtifactCard, 0, len(artifacts)),
		Parameters: uiParameters(job.Parameters, d.catalog, job.Workload),
		Session:    sessionViewFrom(ctx),
	}
	for _, task := range tasks {
		card := TaskCard{ID: task.ID.String(), ChunkIndex: task.ChunkIndex, Status: string(task.Status), Attempt: task.Attempt, MaxAttempts: task.MaxAttempts, LeaseExpiresAt: task.LeaseExpiresAt, StartedAt: task.StartedAt, CompletedAt: task.CompletedAt}
		if task.LeaseOwner != nil {
			card.LeaseOwner = workerNames[*task.LeaseOwner]
			if card.LeaseOwner == "" {
				card.LeaseOwner = "Worker " + shortID(*task.LeaseOwner)
			}
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
		downloadable := previewableArtifact(*job, artifact)
		out.Artifacts = append(out.Artifacts, ArtifactCard{ID: artifact.ID.String(), Kind: string(artifact.Kind), Filename: artifact.Filename, SizeBytes: artifact.SizeBytes, SHA256: artifact.SHA256, Downloadable: downloadable, Diagnostic: diagnostic})
		if artifact.Kind == domain.ArtifactFinalResult && downloadable {
			out.FinalResultAvailable = true
		}
	}
	return out, nil
}

// DownloadableArtifactBelongsToJob applies the same policy used by the UI
// projection: partial diagnostics and the persisted final result are public to
// the operator; source inputs and shards are not exposed through a guessed UI
// URL.
func (d *Dashboard) DownloadableArtifactBelongsToJob(ctx context.Context, jobID, artifactID uuid.UUID) (bool, error) {
	job, err := d.read.GetJob(ctx, jobID)
	if err != nil {
		return false, err
	}
	// Not the caller's job (and not admin): treat as if the artifact is absent.
	if err := authorizeJobAccess(ctx, job); err != nil {
		return false, nil //nolint:nilerr // masking the authz error as "not found" is intentional
	}
	artifacts, err := d.read.ListArtifactsByJob(ctx, jobID)
	if err != nil {
		return false, err
	}
	for _, a := range artifacts {
		if a.ID == artifactID && previewableArtifact(*job, a) {
			return true, nil
		}
	}
	return false, nil
}

func jobCard(job domain.Job, tasks []domain.Task) JobCard {
	c := JobCard{ID: job.ID.String(), Workload: job.Workload, CreatedAt: job.CreatedAt, CompletedAt: job.CompletedAt, ReducerStartedAt: job.ReducerStartedAt}
	if job.ErrorCode != nil {
		c.ErrorCode = *job.ErrorCode
	}
	if job.ErrorMessage != nil {
		c.ErrorMessage = *job.ErrorMessage
	}
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

func uiParameters(parameters map[string]any, catalog *workloads.Catalog, workload string) []ParameterCard {
	labels := map[string]string{
		"query_smiles":        "Target SMILES",
		"query_id":            "Target ChEMBL ID",
		"top_k":               "Global top-k",
		"threshold":           "Similarity threshold",
		"threshold_direction": "Threshold direction",
		"min_molwt":           "Minimum molecular weight",
		"max_molwt":           "Maximum molecular weight",
		"skip_invalid":        "Skip invalid molecules",
		"block_size":          "Block size",
	}
	keys := make([]string, 0, len(parameters))
	declared := declaredParameterNames(catalog, workload)
	for key := range parameters {
		if declared != nil && !declared[key] {
			// Only schema-declared scientific parameters may reach the browser;
			// anything else could carry internal coordinator state.
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]ParameterCard, 0, len(keys))
	for _, key := range keys {
		value, ok := parameters[key]
		if !ok {
			continue
		}
		formatted, ok := formatUIParameter(value)
		if !ok {
			continue
		}
		label := labels[key]
		if label == "" {
			label = strings.ReplaceAll(key, "_", " ")
		}
		out = append(out, ParameterCard{Label: label, Value: formatted})
	}
	return out
}

func declaredParameterNames(catalog *workloads.Catalog, workload string) map[string]bool {
	if catalog == nil || workload == "" {
		return nil
	}
	item := catalog.ByName(workload)
	if item == nil {
		return nil
	}
	properties, ok := item.Parameters["properties"].(map[string]any)
	if !ok {
		return nil
	}
	declared := make(map[string]bool, len(properties))
	for name := range properties {
		declared[name] = true
	}
	return declared
}

func formatUIParameter(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case int, int64, float64, bool:
		return fmt.Sprint(typed), true
	default:
		return "", false
	}
}

func shortID(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}
