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
	tx        TxManager
	clk       Clock
}

func NewUploadArtifact(tasks TaskRepository, artifacts ArtifactRepository,
	blobs BlobStore, tx TxManager, clk Clock) *UploadArtifact {
	return &UploadArtifact{tasks: tasks, artifacts: artifacts, blobs: blobs, tx: tx, clk: clk}
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
	// A client can retry a PUT after losing the response. Return the one durable
	// result for this lease attempt instead of storing duplicate artifacts.
	existing, err := uc.artifacts.FindPartialResult(ctx, in.TaskID, in.Attempt)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
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

	// The stream may take longer than the lease. Lock the task while re-checking
	// ownership and inserting metadata: completion or another upload cannot race
	// this final decision. The database unique index is a second line of defence.
	var durable *domain.Artifact
	err = uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		current, err := uc.tasks.GetForUpdate(ctx, in.TaskID)
		if err != nil {
			return err
		}
		if !current.IsLeaseHeldBy(in.WorkerID, in.Attempt, uc.clk.Now()) {
			return domain.ErrLeaseConflict
		}
		existing, err := uc.artifacts.FindPartialResult(ctx, in.TaskID, in.Attempt)
		if err != nil {
			return err
		}
		if existing != nil {
			durable = existing
			return nil
		}
		if err := uc.artifacts.Insert(ctx, art); err != nil {
			return err
		}
		durable = art
		return nil
	})
	if err != nil {
		_ = uc.blobs.Delete(ctx, art.StorageKey)
		return nil, err
	}
	if durable != art {
		// Another request won the race while this stream was being written.
		_ = uc.blobs.Delete(ctx, art.StorageKey)
	}
	return durable, nil
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
