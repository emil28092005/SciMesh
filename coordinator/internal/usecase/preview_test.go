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

func newPreviewHarness() (*usecase.PreviewArtifact, *memstore.JobRepo, *memstore.ArtifactRepo, *memstore.BlobStore) {
	jobs := memstore.NewJobRepo()
	tasks := memstore.NewTaskRepo()
	workers := memstore.NewWorkerRepo()
	artifacts := memstore.NewArtifactRepo()
	blobs := memstore.NewBlobStore()
	return usecase.NewPreviewArtifact(memstore.NewUIReadRepo(jobs, tasks, workers, artifacts), blobs), jobs, artifacts, blobs
}

func previewJob(t *testing.T, jobs *memstore.JobRepo, status domain.JobStatus) uuid.UUID {
	t.Helper()
	job := &domain.Job{ID: uuid.New(), Workload: "similarity-search", Status: status, CreatedAt: time.Now().UTC()}
	if err := jobs.Insert(context.Background(), job); err != nil {
		t.Fatalf("insert preview job: %v", err)
	}
	return job.ID
}

func previewArtifact(t *testing.T, artifacts *memstore.ArtifactRepo, blobs *memstore.BlobStore, jobID uuid.UUID, kind domain.ArtifactKind, filename, contentType, contents string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	sha, size, err := blobs.Put(context.Background(), id.String(), strings.NewReader(contents))
	if err != nil {
		t.Fatalf("store preview artifact: %v", err)
	}
	artifact := &domain.Artifact{ID: id, JobID: jobID, Kind: kind, Filename: filename, StorageKey: id.String(), ContentType: contentType, SizeBytes: size, SHA256: sha, CreatedAt: time.Now().UTC()}
	if err := artifacts.Insert(context.Background(), artifact); err != nil {
		t.Fatalf("insert preview artifact: %v", err)
	}
	return id
}

func TestPreviewArtifactRendersBoundedCSV(t *testing.T) {
	preview, jobs, artifacts, blobs := newPreviewHarness()
	jobID := previewJob(t, jobs, domain.JobRunning)
	var csv strings.Builder
	csv.WriteString("chembl_id,score\n")
	for i := 0; i < 40; i++ {
		csv.WriteString("CHEMBL" + strconv.Itoa(i) + ",0.9\n")
	}
	artifactID := previewArtifact(t, artifacts, blobs, jobID, domain.ArtifactPartialResult, "partial.csv", "text/csv; charset=utf-8", csv.String())

	view, err := preview.Execute(context.Background(), jobID, artifactID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !view.Previewable || !view.Diagnostic || !view.Truncated || len(view.Rows) != 30 || view.Headers[0] != "chembl_id" || view.Rows[0][0] != "CHEMBL0" {
		t.Fatalf("unexpected preview: %+v", view)
	}
}

func TestPreviewArtifactCapsStorageReadAndHandlesInvalidCSV(t *testing.T) {
	preview, jobs, artifacts, blobs := newPreviewHarness()
	jobID := previewJob(t, jobs, domain.JobRunning)
	// Fewer than 30 oversized records force the byte cap, rather than the row
	// cap, to stop parsing.
	large := "id,value\n" + strings.Repeat("row,"+strings.Repeat("x", 5*1024)+"\n", 20)
	largeID := previewArtifact(t, artifacts, blobs, jobID, domain.ArtifactPartialResult, "large.csv", "text/csv", large)
	view, err := preview.Execute(context.Background(), jobID, largeID)
	if err != nil || !view.Previewable || !view.Truncated || len(view.Rows) > 30 {
		t.Fatalf("large preview = (%+v, %v)", view, err)
	}
	invalidID := previewArtifact(t, artifacts, blobs, jobID, domain.ArtifactPartialResult, "broken.csv", "text/csv", "\"unterminated")
	invalid, err := preview.Execute(context.Background(), jobID, invalidID)
	if err != nil || invalid.Previewable || invalid.Reason == "" {
		t.Fatalf("invalid preview = (%+v, %v)", invalid, err)
	}
}

func TestPreviewArtifactRejectsOtherJobsAndNonResults(t *testing.T) {
	preview, jobs, artifacts, blobs := newPreviewHarness()
	jobA := previewJob(t, jobs, domain.JobRunning)
	jobB := previewJob(t, jobs, domain.JobRunning)
	partialID := previewArtifact(t, artifacts, blobs, jobA, domain.ArtifactPartialResult, "partial.csv", "text/csv", "a,b\n1,2\n")
	if _, err := preview.Execute(context.Background(), jobB, partialID); !errors.Is(err, domain.ErrArtifactNotFound) {
		t.Fatalf("cross-job preview error = %v", err)
	}
	inputID := previewArtifact(t, artifacts, blobs, jobA, domain.ArtifactInput, "input.csv", "text/csv", "a,b\n1,2\n")
	if _, err := preview.Execute(context.Background(), jobA, inputID); !errors.Is(err, domain.ErrArtifactNotFound) {
		t.Fatalf("input preview error = %v", err)
	}
}

func TestPreviewArtifactExposesOnlyPersistedCompletedFinalResult(t *testing.T) {
	preview, jobs, artifacts, blobs := newPreviewHarness()
	jobID := previewJob(t, jobs, domain.JobReducing)
	finalID := previewArtifact(t, artifacts, blobs, jobID, domain.ArtifactFinalResult, "final.csv", "text/csv", "rank,chembl_id\n1,CHEMBL1\n")
	if _, err := preview.Execute(context.Background(), jobID, finalID); !errors.Is(err, domain.ErrArtifactNotFound) {
		t.Fatalf("uncompleted final preview error = %v", err)
	}
	if err := jobs.CompleteWithResult(context.Background(), jobID, finalID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	view, err := preview.Execute(context.Background(), jobID, finalID)
	if err != nil || !view.Previewable || view.Diagnostic {
		t.Fatalf("completed final preview = (%+v, %v)", view, err)
	}
	if err := jobs.FailReduction(context.Background(), jobID, "reducer_failed", "final result reduction failed", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := preview.Execute(context.Background(), jobID, finalID); !errors.Is(err, domain.ErrArtifactNotFound) {
		t.Fatalf("failed reducer preview error = %v", err)
	}
}
