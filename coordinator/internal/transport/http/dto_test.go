package http

import (
	"testing"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

func TestJobProgressResponseExposesFinalResultOnlyWhenCompleted(t *testing.T) {
	id := uuid.New()
	result := uuid.New()
	progress := domain.JobProgress{Job: domain.Job{
		ID: id, Status: domain.JobCompleted, ResultArtifactID: &result,
	}, Total: 1, Done: 1}
	if got, want := toJobProgressResponse(progress).ResultURI, "/jobs/"+id.String()+"/result"; got != want {
		t.Fatalf("result URI = %q, want %q", got, want)
	}

	progress.Job.Status = domain.JobReducing
	if got := toJobProgressResponse(progress).ResultURI; got != "" {
		t.Fatalf("reducing job exposes result URI %q", got)
	}
}
