package usecase_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/memstore"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

func newPreviewHarness() (*usecase.PreviewArtifact, *memstore.JobRepo, *memstore.TaskRepo, *memstore.ArtifactRepo, *memstore.BlobStore) {
	jobs := memstore.NewJobRepo()
	tasks := memstore.NewTaskRepo()
	work := memstore.NewWorkerRepo()
	arts := memstore.NewArtifactRepo()
	blobs := memstore.NewBlobStore()
	read := memstore.NewUIReadRepo(jobs, tasks, work, arts)
	return usecase.NewPreviewArtifact(read, blobs), jobs, tasks, arts, blobs
}

func mustInsertJob(t *testing.T, jobs *memstore.JobRepo, status domain.JobStatus) uuid.UUID {
	t.Helper()
	job := &domain.Job{ID: uuid.New(), Workload: "similarity-search", Status: status, CreatedAt: time.Now()}
	if err := jobs.Insert(context.Background(), job); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	return job.ID
}

func mustCompleteJob(t *testing.T, jobs *memstore.JobRepo, tasks *memstore.TaskRepo) uuid.UUID {
	t.Helper()
	jobID := mustInsertJob(t, jobs, domain.JobRunning)
	task := &domain.Task{
		ID: uuid.New(), JobID: jobID, ChunkIndex: 0, Workload: "similarity-search",
		Status: domain.TaskCompleted, MaxAttempts: 3, CreatedAt: time.Now(),
	}
	if err := tasks.InsertBatch(context.Background(), []*domain.Task{task}); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	return jobID
}

func mustInsertArtifact(t *testing.T, arts *memstore.ArtifactRepo, blobs *memstore.BlobStore,
	jobID uuid.UUID, kind domain.ArtifactKind, filename, contentType, body string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	sha, size, err := blobs.Put(context.Background(), id.String(), strings.NewReader(body))
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	art := &domain.Artifact{
		ID: id, JobID: jobID, Kind: kind, Filename: filename,
		StorageKey: id.String(), ContentType: contentType,
		SizeBytes: size, SHA256: sha, CreatedAt: time.Now(),
	}
	if err := arts.Insert(context.Background(), art); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}
	return id
}

func TestPreviewArtifactRendersCSVRows(t *testing.T) {
	preview, jobs, _, arts, blobs := newPreviewHarness()
	jobID := mustInsertJob(t, jobs, domain.JobRunning)
	artID := mustInsertArtifact(t, arts, blobs, jobID, domain.ArtifactPartialResult, "result.csv", "text/csv",
		"chembl_id,score\nCHEMBL1,0.9\nCHEMBL2,0.8\n")

	view, err := preview.Execute(context.Background(), jobID, artID)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !view.Previewable {
		t.Fatalf("expected previewable, reason=%q", view.Reason)
	}
	if view.Truncated {
		t.Error("small CSV should not be truncated")
	}
	if len(view.Headers) != 2 || view.Headers[0] != "chembl_id" {
		t.Errorf("headers = %v", view.Headers)
	}
	if len(view.Rows) != 2 || view.Rows[0][0] != "CHEMBL1" {
		t.Errorf("rows = %v", view.Rows)
	}
}

func TestPreviewArtifactTruncatesAt30Rows(t *testing.T) {
	preview, jobs, _, arts, blobs := newPreviewHarness()
	jobID := mustInsertJob(t, jobs, domain.JobRunning)

	var sb strings.Builder
	sb.WriteString("id,value\n")
	for i := 0; i < 40; i++ {
		sb.WriteString("R" + strconv.Itoa(i) + ",v\n")
	}
	artID := mustInsertArtifact(t, arts, blobs, jobID, domain.ArtifactPartialResult, "result.csv", "text/csv", sb.String())

	view, err := preview.Execute(context.Background(), jobID, artID)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(view.Rows) != 30 {
		t.Fatalf("rows = %d, want 30", len(view.Rows))
	}
	if !view.Truncated {
		t.Error("expected truncated for more than 30 data rows")
	}
}

func TestPreviewArtifactTruncatesAt64KiB(t *testing.T) {
	preview, jobs, _, arts, blobs := newPreviewHarness()
	jobID := mustInsertJob(t, jobs, domain.JobRunning)

	var sb strings.Builder
	sb.WriteString("id,value\n")
	row := "row," + strings.Repeat("x", 200) + "\n"
	for sb.Len() < 70*1024 {
		sb.WriteString(row)
	}
	artID := mustInsertArtifact(t, arts, blobs, jobID, domain.ArtifactPartialResult, "result.csv", "text/csv", sb.String())

	view, err := preview.Execute(context.Background(), jobID, artID)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !view.Truncated {
		t.Error("expected truncated for an artifact bigger than 64KiB")
	}
	if len(view.Rows) > 30 {
		t.Errorf("rows = %d, want <= 30", len(view.Rows))
	}
}

func TestPreviewArtifactRejectsNonCSV(t *testing.T) {
	preview, jobs, _, arts, blobs := newPreviewHarness()
	jobID := mustInsertJob(t, jobs, domain.JobRunning)
	artID := mustInsertArtifact(t, arts, blobs, jobID, domain.ArtifactPartialResult,
		"shard-0.tsv", "text/tab-separated-values", "chembl_id\tcanonical_smiles\nCHEMBL1\tCCO\n")

	view, err := preview.Execute(context.Background(), jobID, artID)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if view.Previewable {
		t.Error("non-CSV artifact must not be previewable as text")
	}
	if view.Reason == "" {
		t.Error("expected a friendly reason")
	}
}

func TestPreviewArtifactFailsSafelyOnEmptyArtifact(t *testing.T) {
	preview, jobs, _, arts, blobs := newPreviewHarness()
	jobID := mustInsertJob(t, jobs, domain.JobRunning)
	artID := mustInsertArtifact(t, arts, blobs, jobID, domain.ArtifactPartialResult, "result.csv", "text/csv", "")

	view, err := preview.Execute(context.Background(), jobID, artID)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if view.Previewable {
		t.Error("empty artifact must not be previewable")
	}
	if view.Reason == "" {
		t.Error("expected a friendly reason")
	}
}

func TestPreviewArtifactFailsSafelyOnMalformedCSV(t *testing.T) {
	preview, jobs, _, arts, blobs := newPreviewHarness()
	jobID := mustInsertJob(t, jobs, domain.JobRunning)
	// An unterminated quote makes even the header row unparsable.
	artID := mustInsertArtifact(t, arts, blobs, jobID, domain.ArtifactPartialResult, "result.csv", "text/csv", `"unterminated`)

	view, err := preview.Execute(context.Background(), jobID, artID)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if view.Previewable {
		t.Error("malformed CSV must not be previewable")
	}
	if view.Reason == "" {
		t.Error("expected a friendly reason")
	}
}

func TestPreviewArtifactRejectsCrossJobArtifact(t *testing.T) {
	preview, jobs, _, arts, blobs := newPreviewHarness()
	jobA := mustInsertJob(t, jobs, domain.JobRunning)
	jobB := mustInsertJob(t, jobs, domain.JobRunning)
	artID := mustInsertArtifact(t, arts, blobs, jobA, domain.ArtifactPartialResult, "result.csv", "text/csv", "a,b\n1,2\n")

	if _, err := preview.Execute(context.Background(), jobB, artID); !errors.Is(err, domain.ErrArtifactNotFound) {
		t.Fatalf("err = %v, want ErrArtifactNotFound", err)
	}
}

func TestPreviewArtifactRejectsUncompletedFinalResult(t *testing.T) {
	preview, jobs, _, arts, blobs := newPreviewHarness()
	jobID := mustInsertJob(t, jobs, domain.JobRunning)
	artID := mustInsertArtifact(t, arts, blobs, jobID, domain.ArtifactFinalResult, "final.csv", "text/csv", "a,b\n1,2\n")

	if _, err := preview.Execute(context.Background(), jobID, artID); !errors.Is(err, domain.ErrArtifactNotFound) {
		t.Fatalf("err = %v, want ErrArtifactNotFound", err)
	}
}

func TestPreviewArtifactAllowsFinalResultOnceJobIsCompleted(t *testing.T) {
	preview, jobs, tasks, arts, blobs := newPreviewHarness()
	jobID := mustCompleteJob(t, jobs, tasks)
	artID := mustInsertArtifact(t, arts, blobs, jobID, domain.ArtifactFinalResult, "final.csv", "text/csv", "a,b\n1,2\n")

	view, err := preview.Execute(context.Background(), jobID, artID)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !view.Previewable {
		t.Fatalf("expected previewable, reason=%q", view.Reason)
	}
}
