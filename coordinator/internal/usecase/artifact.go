package usecase

import (
	"context"
	"io"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// UploadArtifact stores a worker's partial-result bytes and records the metadata.
type UploadArtifact struct {
	tasks     TaskRepository
	artifacts ArtifactRepository
	blobs     BlobStore
	clk       Clock
}

func NewUploadArtifact(tasks TaskRepository, artifacts ArtifactRepository,
	blobs BlobStore, clk Clock) *UploadArtifact {
	return &UploadArtifact{tasks: tasks, artifacts: artifacts, blobs: blobs, clk: clk}
}

func (uc *UploadArtifact) Execute(ctx context.Context, in UploadArtifactInput) (*domain.Artifact, error) {
	task, err := uc.tasks.Get(ctx, in.TaskID)
	if err != nil {
		return nil, err
	}
	// Only the worker holding the current lease at this attempt may upload the
	// task's output — the coordinator never trusts an ownership claim on faith.
	if !task.IsLeaseHeldBy(in.WorkerID, in.Attempt, uc.clk.Now()) {
		return nil, domain.ErrLeaseConflict
	}

	taskID := task.ID
	art, err := domain.NewArtifact(task.JobID, &taskID, domain.ArtifactPartialResult,
		in.Filename, in.ContentType, uc.clk.Now())
	if err != nil {
		return nil, err
	}
	attempt := in.Attempt
	art.Attempt = &attempt

	// Stream to storage first: size and checksum are measured here, by us, not
	// taken from the worker. A large shard never sits in memory.
	sum, size, err := uc.blobs.Put(ctx, art.StorageKey, in.Body)
	if err != nil {
		return nil, err
	}
	art.SetContent(sum, size)

	// The stream may take longer than the lease. Re-check after it finishes so
	// an expired worker cannot leave a durable result record behind. Completion
	// performs the same ownership check under its transaction.
	current, err := uc.tasks.Get(ctx, in.TaskID)
	if err != nil {
		_ = uc.blobs.Delete(ctx, art.StorageKey)
		return nil, err
	}
	if !current.IsLeaseHeldBy(in.WorkerID, in.Attempt, uc.clk.Now()) {
		_ = uc.blobs.Delete(ctx, art.StorageKey)
		return nil, domain.ErrLeaseConflict
	}

	// Persist the record. If that fails the blob would be an orphan, so remove it.
	if err := uc.artifacts.Insert(ctx, art); err != nil {
		_ = uc.blobs.Delete(ctx, art.StorageKey)
		return nil, err
	}
	return art, nil
}

// DownloadArtifact returns an artifact's metadata together with a reader over
// its bytes. The caller must close the reader.
type DownloadArtifact struct {
	artifacts ArtifactRepository
	blobs     BlobStore
}

func NewDownloadArtifact(artifacts ArtifactRepository, blobs BlobStore) *DownloadArtifact {
	return &DownloadArtifact{artifacts: artifacts, blobs: blobs}
}

func (uc *DownloadArtifact) Execute(ctx context.Context, id uuid.UUID) (*domain.Artifact, io.ReadCloser, error) {
	a, err := uc.artifacts.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	rc, err := uc.blobs.Open(ctx, a.StorageKey)
	if err != nil {
		return nil, nil, err
	}
	return a, rc, nil
}
