package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

func TestWorkloadSettingsRepoRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := NewWorkloadSettingsRepo(db)

	// No override: enabled by default.
	enabled, err := repo.GetEnabled(ctx, "similarity-search")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Error("workload without an override must be enabled")
	}

	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	if err := repo.SetEnabled(ctx, "similarity-search", false, now); err != nil {
		t.Fatal(err)
	}
	enabled, err = repo.GetEnabled(ctx, "similarity-search")
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Error("workload must be disabled after the override")
	}

	// Upsert flips it back and updates the timestamp.
	later := now.Add(time.Hour)
	if err := repo.SetEnabled(ctx, "similarity-search", true, later); err != nil {
		t.Fatal(err)
	}
	enabled, err = repo.GetEnabled(ctx, "similarity-search")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Error("workload must be re-enabled after the upsert")
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Workload != "similarity-search" || !list[0].Enabled {
		t.Errorf("list = %+v, want the single re-enabled override", list)
	}
}

func TestWorkerSetTrust(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := NewWorkerRepo(db)

	worker, err := domain.NewWorker("lab-node", []string{"similarity-search"}, fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Insert(ctx, worker); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetTrust(ctx, worker.ID, domain.WorkerUntrusted); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrustLevel != domain.WorkerUntrusted {
		t.Errorf("trust = %q, want untrusted", got.TrustLevel)
	}
	if err := repo.SetTrust(ctx, worker.ID, domain.WorkerTrusted); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetTrust(ctx, uuid.New(), domain.WorkerTrusted); err != domain.ErrWorkerNotFound {
		t.Errorf("unknown worker trust err = %v, want ErrWorkerNotFound", err)
	}
}
