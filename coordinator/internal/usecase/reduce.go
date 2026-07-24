package usecase

import (
	"bytes"
	"context"
	"io"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/reducer"
)

// ReduceJob turns completed coordinator-owned partial artifacts into one final
// artifact. It performs no worker I/O and never trusts a worker URI or path.
type ReduceJob struct {
	jobs      JobRepository
	tasks     TaskRepository
	artifacts ArtifactRepository
	blobs     BlobStore
	tx        TxManager
	clock     Clock
}

func NewReduceJob(jobs JobRepository, tasks TaskRepository, artifacts ArtifactRepository,
	blobs BlobStore, tx TxManager, clock Clock) *ReduceJob {
	return &ReduceJob{jobs: jobs, tasks: tasks, artifacts: artifacts, blobs: blobs, tx: tx, clock: clock}
}

// Execute is idempotent for jobs that are not currently reducing. The worker
// completion path may call it after every result; only the last task changes a
// similarity-search job into reducing state.
func (uc *ReduceJob) Execute(ctx context.Context, jobID uuid.UUID) error {
	claimed, err := uc.jobs.ClaimReduction(ctx, jobID, uc.clock.Now())
	if err != nil || !claimed {
		return err
	}
	job, err := uc.jobs.Get(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status != domain.JobReducing {
		return nil
	}
	if job.Workload != "similarity-search" {
		return uc.fail(ctx, jobID)
	}

	completed, err := uc.tasks.ListCompleted(ctx, jobID)
	if err != nil {
		return uc.fail(ctx, jobID)
	}
	if len(completed) == 0 {
		return uc.fail(ctx, jobID)
	}
	readers := make([]io.Reader, 0, len(completed))
	closers := make([]io.Closer, 0, len(completed))
	for _, task := range completed {
		if task.ResultArtifactID == nil {
			closeAll(closers)
			return uc.fail(ctx, jobID)
		}
		artifact, err := uc.artifacts.Get(ctx, *task.ResultArtifactID)
		if err != nil || artifact.JobID != jobID || artifact.TaskID == nil || *artifact.TaskID != task.ID || artifact.Kind != domain.ArtifactPartialResult {
			closeAll(closers)
			return uc.fail(ctx, jobID)
		}
		body, err := uc.blobs.Open(ctx, artifact.StorageKey)
		if err != nil {
			closeAll(closers)
			return uc.fail(ctx, jobID)
		}
		readers = append(readers, body)
		closers = append(closers, body)
	}
	output, reduceErr := reducer.ReduceSimilaritySearch(readers, job.Parameters)
	closeAll(closers)
	if reduceErr != nil {
		return uc.fail(ctx, jobID)
	}

	final, err := domain.NewArtifact(jobID, nil, domain.ArtifactFinalResult, "similarity-search.csv", "text/csv", uc.clock.Now())
	if err != nil {
		return uc.fail(ctx, jobID)
	}
	sum, size, err := uc.blobs.Put(ctx, final.StorageKey, bytes.NewReader(output))
	if err != nil {
		return uc.fail(ctx, jobID)
	}
	final.SetContent(sum, size)
	if err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := uc.artifacts.Insert(ctx, final); err != nil {
			return err
		}
		return uc.jobs.CompleteWithResult(ctx, jobID, final.ID, uc.clock.Now())
	}); err != nil {
		_ = uc.blobs.Delete(ctx, final.StorageKey)
		return err
	}
	return nil
}

func (uc *ReduceJob) fail(ctx context.Context, jobID uuid.UUID) error {
	// The public state carries a stable sanitized failure, never parser/storage
	// internals that may include local paths or implementation details.
	return uc.jobs.FailReduction(ctx, jobID, "reducer_failed", "final result reduction failed", uc.clock.Now())
}

func closeAll(closers []io.Closer) {
	for _, closer := range closers {
		_ = closer.Close()
	}
}

type GetJobResult struct {
	jobs     JobRepository
	download *DownloadArtifact
}

func NewGetJobResult(jobs JobRepository, download *DownloadArtifact) *GetJobResult {
	return &GetJobResult{jobs: jobs, download: download}
}

func (uc *GetJobResult) Execute(ctx context.Context, jobID uuid.UUID) (*domain.Artifact, io.ReadCloser, error) {
	job, err := uc.jobs.Get(ctx, jobID)
	if err != nil {
		return nil, nil, err
	}
	if job.Status != domain.JobCompleted || job.ResultArtifactID == nil {
		return nil, nil, domain.ErrArtifactNotFound
	}
	return uc.download.Execute(ctx, *job.ResultArtifactID)
}
