package usecase_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/memstore"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

func newDashboardWithWorkers() (*usecase.Dashboard, *memstore.WorkerRepo) {
	jobs := memstore.NewJobRepo()
	tasks := memstore.NewTaskRepo()
	workers := memstore.NewWorkerRepo()
	artifacts := memstore.NewArtifactRepo()
	return usecase.NewDashboard(memstore.NewUIReadRepo(jobs, tasks, workers, artifacts), testCatalog()), workers
}

func seedWorker(t *testing.T, workers *memstore.WorkerRepo, owner *uuid.UUID, name string) {
	t.Helper()
	w := &domain.Worker{
		ID:              uuid.New(),
		Name:            name,
		Capabilities:    []string{"similarity-search"},
		Status:          domain.WorkerOnline,
		OwnerID:         owner,
		LastHeartbeatAt: time.Now().UTC(),
	}
	if err := workers.Insert(context.Background(), w); err != nil {
		t.Fatalf("insert worker: %v", err)
	}
}

func TestOverviewSplitsMyWorkers(t *testing.T) {
	dash, workers := newDashboardWithWorkers()
	alice, bob := uuid.New(), uuid.New()
	seedWorker(t, workers, &alice, "alice-box")
	seedWorker(t, workers, &bob, "bob-box")
	seedWorker(t, workers, nil, "lab-shared") // owner-less shared-token worker

	// A plain user sees the whole fleet, but MyWorkers holds only their own.
	v, err := dash.Overview(userCtx(alice, "user"), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Workers) != 3 {
		t.Errorf("fleet shows %d workers, want 3", len(v.Workers))
	}
	if len(v.MyWorkers) != 1 || v.MyWorkers[0].Name != "alice-box" {
		t.Errorf("MyWorkers = %+v, want only alice-box", v.MyWorkers)
	}

	// An admin is not owner-scoped: they get the fleet and no personal list.
	if av, _ := dash.Overview(userCtx(uuid.New(), "admin"), 20); len(av.MyWorkers) != 0 || len(av.Workers) != 3 {
		t.Errorf("admin MyWorkers=%d Workers=%d, want 0 and 3", len(av.MyWorkers), len(av.Workers))
	}

	// A basic-auth operator (no requester) also gets no personal list.
	if ov, _ := dash.Overview(context.Background(), 20); len(ov.MyWorkers) != 0 {
		t.Errorf("operator MyWorkers=%d, want 0", len(ov.MyWorkers))
	}
}
