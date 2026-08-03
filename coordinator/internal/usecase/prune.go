package usecase

import (
	"context"
	"time"
)

// PruneArtifacts removes completed or failed jobs older than `olderThan` and
// every artifact they own: database rows cascade, blob files are deleted
// explicitly. It returns what was freed so the admin console can report it.
type PruneArtifacts struct {
	jobs  JobRepository
	read  UIReadRepository
	blobs BlobStore
	clk   Clock
}

func NewPruneArtifacts(jobs JobRepository, read UIReadRepository, blobs BlobStore, clk Clock) *PruneArtifacts {
	return &PruneArtifacts{jobs: jobs, read: read, blobs: blobs, clk: clk}
}

type PruneResult struct {
	Jobs       int   `json:"jobs"`
	Artifacts  int   `json:"artifacts"`
	FreedBytes int64 `json:"freed_bytes"`
}

// Execute deletes finished jobs whose completion timestamp is older than the
// cutoff. Jobs that are still active are never touched.
func (uc *PruneArtifacts) Execute(ctx context.Context, olderThan time.Duration) (PruneResult, error) {
	cutoff := uc.clk.Now().Add(-olderThan)
	jobs, err := uc.jobs.ListCompletedBefore(ctx, cutoff)
	if err != nil {
		return PruneResult{}, err
	}
	out := PruneResult{}
	for _, job := range jobs {
		artifacts, err := uc.read.ListArtifactsByJob(ctx, job.ID)
		if err != nil {
			return out, err
		}
		for _, artifact := range artifacts {
			if err := uc.blobs.Delete(ctx, artifact.StorageKey); err != nil {
				return out, err
			}
			out.FreedBytes += artifact.SizeBytes
			out.Artifacts++
		}
		if err := uc.jobs.Delete(ctx, job.ID); err != nil {
			return out, err
		}
		out.Jobs++
	}
	return out, nil
}
