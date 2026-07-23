package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// Job operations: the submitter-facing lifecycle of a whole submission.
//
//	CreateJob    register a job and fan it out into tasks
//	GetJobStatus aggregate progress
//	ListResults  completed manifests, ordered for the stitcher
//	StitchJob    merge partial results into the final artifact

// --- CreateJob -----------------------------------------------------------

type CreateJob struct {
	jobs  JobRepository
	tasks TaskRepository
	tx    TxManager
	clock Clock
}

func NewCreateJob(jobs JobRepository, tasks TaskRepository, tx TxManager, clock Clock) *CreateJob {
	return &CreateJob{jobs: jobs, tasks: tasks, tx: tx, clock: clock}
}

// Execute builds the job and its tasks, then writes them in one transaction.
// The all-or-none guarantee comes from TxManager: a half-created job would
// leave chunks no worker could ever complete.
func (uc *CreateJob) Execute(ctx context.Context, in CreateJobInput) (*domain.Job, error) {
	chunks := make([]domain.ChunkSpec, 0, len(in.Chunks))
	for _, c := range in.Chunks {
		chunks = append(chunks, domain.ChunkSpec(c))
	}

	job, tasks, err := domain.NewJobWithTasks(in.Workload, in.InputURI, in.Parameters, chunks, uc.clock.Now())
	if err != nil {
		return nil, err
	}

	err = uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := uc.jobs.Insert(ctx, job); err != nil {
			return err
		}
		return uc.tasks.InsertBatch(ctx, tasks)
	})
	if err != nil {
		return nil, err
	}
	return job, nil
}

// --- GetJobStatus --------------------------------------------------------

type GetJobStatus struct {
	jobs  JobRepository
	tasks TaskRepository
}

// --- CancelJob -----------------------------------------------------------

type CancelJob struct {
	jobs  JobRepository
	tasks TaskRepository
	tx    TxManager
	clock Clock
}

func NewCancelJob(jobs JobRepository, tasks TaskRepository, tx TxManager, clock Clock) *CancelJob {
	return &CancelJob{jobs: jobs, tasks: tasks, tx: tx, clock: clock}
}

// Execute stops a job atomically. Completed and finally failed tasks are kept
// as historical evidence; all other tasks are cancelled, including leased and
// running ones. A repeated cancel of an already cancelled job is idempotent.
func (uc *CancelJob) Execute(ctx context.Context, jobID uuid.UUID) (int64, error) {
	now := uc.clock.Now()
	var cancelled int64
	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		job, err := uc.jobs.Get(ctx, jobID)
		if err != nil {
			return err
		}
		if job.Status == domain.JobCancelled {
			return nil
		}
		if job.Status == domain.JobCompleted || job.Status == domain.JobFailed {
			return domain.ErrJobNotCancellable
		}
		cancelled, err = uc.tasks.CancelByJob(ctx, jobID, now)
		if err != nil {
			return err
		}
		return uc.jobs.UpdateStatus(ctx, jobID, domain.JobCancelled, &now)
	})
	return cancelled, err
}

func NewGetJobStatus(jobs JobRepository, tasks TaskRepository) *GetJobStatus {
	return &GetJobStatus{jobs: jobs, tasks: tasks}
}

func (uc *GetJobStatus) Execute(ctx context.Context, jobID uuid.UUID) (domain.JobProgress, error) {
	job, err := uc.jobs.Get(ctx, jobID)
	if err != nil {
		return domain.JobProgress{}, err
	}
	counts, err := uc.tasks.CountByStatus(ctx, jobID)
	if err != nil {
		return domain.JobProgress{}, err
	}
	return progressFrom(*job, counts), nil
}

// --- ListResults ---------------------------------------------------------

type ListResults struct {
	tasks TaskRepository
}

func NewListResults(tasks TaskRepository) *ListResults {
	return &ListResults{tasks: tasks}
}

// Execute preserves chunk_index order: the stitcher merges these into one
// artifact, and a non-deterministic order would make the final result depend on
// which worker happened to finish first.
func (uc *ListResults) Execute(ctx context.Context, jobID uuid.UUID) ([]domain.ResultManifest, error) {
	tasks, err := uc.tasks.ListCompleted(ctx, jobID)
	if err != nil {
		return nil, err
	}

	manifests := make([]domain.ResultManifest, 0, len(tasks))
	for _, t := range tasks {
		if t.ResultArtifactID == nil {
			continue // a completed task always references its result; skip defensively
		}
		manifests = append(manifests, domain.ResultManifest{
			TaskID:           t.ID,
			ChunkIndex:       t.ChunkIndex,
			ResultArtifactID: *t.ResultArtifactID,
			Metrics:          t.Metrics,
		})
	}
	return manifests, nil
}

// --- StitchJob -----------------------------------------------------------

// StitchJob merges every chunk's partial result into the job's final artifact.
// For similarity search that means concatenating each worker's local top-k,
// sorting by similarity, and keeping the global top-k — the distributed result
// must match what a single local run would produce.
type StitchJob struct {
	results *ListResults
}

func NewStitchJob(results *ListResults) *StitchJob {
	return &StitchJob{results: results}
}

// Execute returns the URI of the assembled artifact.
//
// TODO(phase 6): fetch each manifest's CSV, merge, and persist the result.
func (uc *StitchJob) Execute(ctx context.Context, jobID uuid.UUID) (string, error) {
	if _, err := uc.results.Execute(ctx, jobID); err != nil {
		return "", err
	}
	return "", ErrNotImplemented
}

// --- shared helpers ------------------------------------------------------

// progressFrom turns a status histogram into the domain's progress view.
func progressFrom(job domain.Job, counts map[domain.TaskStatus]int) domain.JobProgress {
	p := domain.JobProgress{
		Job:     job,
		Pending: counts[domain.TaskPending],
		// Leased and running are both "in flight" for progress purposes.
		Leased:    counts[domain.TaskLeased] + counts[domain.TaskRunning],
		Done:      counts[domain.TaskCompleted],
		Failed:    counts[domain.TaskFailed],
		Cancelled: counts[domain.TaskCancelled],
	}
	for _, n := range counts {
		p.Total += n
	}
	return p
}

// syncJobStatus recomputes a job's status from its task counts and persists it.
// Shared by CompleteTask and FailTask so both close a job by the same rule —
// the rule itself lives in domain.JobProgress.DeriveStatus.
func syncJobStatus(ctx context.Context, jobs JobRepository, tasks TaskRepository,
	jobID uuid.UUID, now time.Time) error {

	counts, err := tasks.CountByStatus(ctx, jobID)
	if err != nil {
		return err
	}
	status := progressFrom(domain.Job{}, counts).DeriveStatus()

	var completedAt *time.Time
	if status == domain.JobCompleted || status == domain.JobFailed {
		completedAt = &now
	}
	return jobs.UpdateStatus(ctx, jobID, status, completedAt)
}
