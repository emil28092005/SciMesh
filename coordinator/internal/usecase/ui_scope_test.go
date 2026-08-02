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

func TestOverviewScopesJobsByOwner(t *testing.T) {
	dash, jobs := newDashboard()
	alice, bob := uuid.New(), uuid.New()
	ownedJob(t, jobs, alice)
	ownedJob(t, jobs, bob)

	// A plain user sees only their own job.
	v, err := dash.Overview(userCtx(alice, "user"), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Jobs) != 1 {
		t.Errorf("alice sees %d jobs, want 1", len(v.Jobs))
	}

	// An admin sees every job.
	if v, _ := dash.Overview(userCtx(uuid.New(), "admin"), 20); len(v.Jobs) != 2 {
		t.Errorf("admin sees %d jobs, want 2", len(v.Jobs))
	}

	// No requester (basic-auth operator) sees every job — unchanged behaviour.
	if v, _ := dash.Overview(context.Background(), 20); len(v.Jobs) != 2 {
		t.Errorf("operator sees %d jobs, want 2", len(v.Jobs))
	}
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
