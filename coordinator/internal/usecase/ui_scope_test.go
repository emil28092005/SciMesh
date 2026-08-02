package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/authctx"
	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/memstore"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

func newDashboard() (*usecase.Dashboard, *memstore.JobRepo) {
	jobs := memstore.NewJobRepo()
	tasks := memstore.NewTaskRepo()
	workers := memstore.NewWorkerRepo()
	artifacts := memstore.NewArtifactRepo()
	return usecase.NewDashboard(memstore.NewUIReadRepo(jobs, tasks, workers, artifacts), testCatalog()), jobs
}

func ownedJob(t *testing.T, jobs *memstore.JobRepo, owner uuid.UUID) uuid.UUID {
	t.Helper()
	o := owner
	job := &domain.Job{ID: uuid.New(), Workload: "similarity-search", Status: domain.JobRunning, OwnerID: &o, CreatedAt: time.Now().UTC()}
	if err := jobs.Insert(context.Background(), job); err != nil {
		t.Fatalf("insert owned job: %v", err)
	}
	return job.ID
}

func userCtx(id uuid.UUID, role string) context.Context {
	return authctx.With(context.Background(), authctx.Requester{UserID: id, Role: role})
}

func TestJobDetailRejectsAnotherUsersJob(t *testing.T) {
	dash, jobs := newDashboard()
	alice, bob := uuid.New(), uuid.New()
	jobID := ownedJob(t, jobs, alice)

	// Bob cannot open Alice's job.
	if _, err := dash.JobDetail(userCtx(bob, "user"), jobID); !errors.Is(err, domain.ErrJobNotFound) {
		t.Errorf("bob: got %v, want ErrJobNotFound", err)
	}
	// Alice can.
	if _, err := dash.JobDetail(userCtx(alice, "user"), jobID); err != nil {
		t.Errorf("alice: unexpected error %v", err)
	}
	// Admin can.
	if _, err := dash.JobDetail(userCtx(uuid.New(), "admin"), jobID); err != nil {
		t.Errorf("admin: unexpected error %v", err)
	}
}
