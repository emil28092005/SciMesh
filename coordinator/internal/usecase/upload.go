package usecase

import (
	"context"
	"fmt"
	"io"
	"math"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/chunk"
	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// SubmitDataset accepts an uploaded dataset, splits it into shard artifacts, and
// creates the job with one task per shard — the coordinator-side counterpart of
// a client submitting pre-chunked URIs.
type SubmitDataset struct {
	blobs       BlobStore
	artifacts   ArtifactRepository
	jobs        JobRepository
	tasks       TaskRepository
	tx          TxManager
	clk         Clock
	maxAttempts int
}

func NewSubmitDataset(blobs BlobStore, artifacts ArtifactRepository, jobs JobRepository,
	tasks TaskRepository, tx TxManager, clk Clock, maxAttempts int) *SubmitDataset {
	return &SubmitDataset{blobs: blobs, artifacts: artifacts, jobs: jobs, tasks: tasks, tx: tx, clk: clk, maxAttempts: maxAttempts}
}

func (uc *SubmitDataset) Execute(ctx context.Context, in SubmitDatasetInput) (SubmitDatasetResult, error) {
	if err := validateUploadedWorkload(in.Workload, in.Parameters); err != nil {
		return SubmitDatasetResult{}, err
	}
	if uc.maxAttempts < 1 {
		return SubmitDatasetResult{}, domain.ErrInvalidInput
	}
	now := uc.clk.Now()

	job, err := domain.NewUploadedJob(in.Workload, in.Parameters, now)
	if err != nil {
		return SubmitDatasetResult{}, err
	}

	// Everything written to blob storage, so a failed transaction can undo it.
	var putKeys []string
	cleanup := func() {
		for _, k := range putKeys {
			_ = uc.blobs.Delete(ctx, k)
		}
	}

	// 1. Stream the upload into the input artifact; we measure size and sha256.
	input, err := domain.NewArtifact(job.ID, nil, domain.ArtifactInput, in.Filename, in.ContentType, now)
	if err != nil {
		return SubmitDatasetResult{}, err
	}
	sum, size, err := uc.blobs.Put(ctx, input.StorageKey, in.Body)
	if err != nil {
		return SubmitDatasetResult{}, err
	}
	putKeys = append(putKeys, input.StorageKey)
	input.SetContent(sum, size)

	// 2. Re-open the stored input and split it into shard artifacts + tasks.
	shards := []*domain.Artifact{}
	tasks := []*domain.Task{}
	rc, err := uc.blobs.Open(ctx, input.StorageKey)
	if err != nil {
		cleanup()
		return SubmitDatasetResult{}, err
	}
	splitErr := chunk.SplitChEMBLTSVLimit(rc, in.RowsPerShard, in.MaxRows, func(index int, shard io.Reader) error {
		art, err := domain.NewArtifact(job.ID, nil, domain.ArtifactShard,
			fmt.Sprintf("shard-%d.tsv", index), in.ContentType, now)
		if err != nil {
			return err
		}
		ssum, ssize, err := uc.blobs.Put(ctx, art.StorageKey, shard)
		if err != nil {
			return err
		}
		putKeys = append(putKeys, art.StorageKey)
		art.SetContent(ssum, ssize)

		task, err := domain.NewShardTask(job.ID, index, in.Workload, art.ID, ssum, in.Parameters, uc.maxAttempts, now)
		if err != nil {
			return err
		}
		shards = append(shards, art)
		tasks = append(tasks, task)
		return nil
	})
	_ = rc.Close()
	if splitErr != nil {
		cleanup()
		// Dataset shape is caller input, not an internal coordinator failure.
		return SubmitDatasetResult{}, domain.ErrInvalidInput
	}

	// 3. Persist job + all artifacts + all tasks atomically.
	err = uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := uc.jobs.Insert(ctx, job); err != nil {
			return err
		}
		if err := uc.artifacts.Insert(ctx, input); err != nil {
			return err
		}
		for _, a := range shards {
			if err := uc.artifacts.Insert(ctx, a); err != nil {
				return err
			}
		}
		return uc.tasks.InsertBatch(ctx, tasks)
	})
	if err != nil {
		cleanup()
		return SubmitDatasetResult{}, err
	}

	return SubmitDatasetResult{
		JobID:           job.ID,
		TaskCount:       len(tasks),
		InputArtifactID: input.ID,
	}, nil
}

// validateUploadedWorkload is deliberately narrow until CTX-07/08/10 adds a
// typed distributed-workload registry. In particular, running similarity-graph
// independently per TSV shard is scientifically wrong: cross-shard pairs would
// be absent from the apparent graph.
func validateUploadedWorkload(workload string, parameters map[string]any) error {
	if workload != "similarity-search" {
		return domain.ErrInvalidInput
	}
	allowed := map[string]struct{}{
		"query_smiles": {}, "top_k": {}, "threshold": {},
		"threshold_direction": {}, "progress_every": {},
	}
	for key := range parameters {
		if _, ok := allowed[key]; !ok {
			return domain.ErrInvalidInput
		}
	}
	query, ok := parameters["query_smiles"].(string)
	if !ok || query == "" || len(query) > 200 {
		return domain.ErrInvalidInput
	}
	if value, ok := parameters["top_k"]; ok && !isPositiveJSONInteger(value) {
		return domain.ErrInvalidInput
	}
	if value, ok := parameters["progress_every"]; ok && !isNonNegativeJSONInteger(value) {
		return domain.ErrInvalidInput
	}
	if value, ok := parameters["threshold"]; ok && !isUnitIntervalNumber(value) {
		return domain.ErrInvalidInput
	}
	if value, ok := parameters["threshold_direction"]; ok && value != "greater" && value != "less" {
		return domain.ErrInvalidInput
	}
	return nil
}

func isPositiveJSONInteger(value any) bool    { return isJSONInteger(value, false) }
func isNonNegativeJSONInteger(value any) bool { return isJSONInteger(value, true) }

func isJSONInteger(value any, allowZero bool) bool {
	var n int64
	switch v := value.(type) {
	case int:
		n = int64(v)
	case int64:
		n = v
	case float64:
		if math.Trunc(v) != v || v > math.MaxInt64 || v < math.MinInt64 {
			return false
		}
		n = int64(v)
	default:
		return false
	}
	return n >= 0 && (allowZero || n > 0)
}

func isUnitIntervalNumber(value any) bool {
	switch v := value.(type) {
	case float64:
		return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1
	case int:
		return v >= 0 && v <= 1
	case int64:
		return v >= 0 && v <= 1
	default:
		return false
	}
}

// GetTaskInput resolves a task's input shard and opens it for streaming. The
// caller closes the reader.
type GetTaskInput struct {
	tasks     TaskRepository
	artifacts ArtifactRepository
	blobs     BlobStore
}

func NewGetTaskInput(tasks TaskRepository, artifacts ArtifactRepository, blobs BlobStore) *GetTaskInput {
	return &GetTaskInput{tasks: tasks, artifacts: artifacts, blobs: blobs}
}

func (uc *GetTaskInput) Execute(ctx context.Context, taskID uuid.UUID) (*domain.Artifact, io.ReadCloser, error) {
	task, err := uc.tasks.Get(ctx, taskID)
	if err != nil {
		return nil, nil, err
	}
	if task.InputArtifactID == nil {
		// A URI-based task keeps its input outside the coordinator.
		return nil, nil, domain.ErrArtifactNotFound
	}
	art, err := uc.artifacts.Get(ctx, *task.InputArtifactID)
	if err != nil {
		return nil, nil, err
	}
	rc, err := uc.blobs.Open(ctx, art.StorageKey)
	if err != nil {
		return nil, nil, err
	}
	return art, rc, nil
}
