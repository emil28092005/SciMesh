package sqlite

import (
	"context"
	"errors"
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
	if err := repo.SetTrust(ctx, uuid.New(), domain.WorkerTrusted); !errors.Is(err, domain.ErrWorkerNotFound) {
		t.Errorf("unknown worker trust err = %v, want ErrWorkerNotFound", err)
	}
}

func TestJobRepoListCompletedBeforeAndDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := NewJobRepo(db)

	old := seedJob(t, db, 2)
	oldTime := fixedTime().Add(-40 * 24 * time.Hour)
	if err := repo.UpdateStatus(ctx, old.ID, domain.JobCompleted, &oldTime); err != nil {
		t.Fatal(err)
	}
	fresh := seedJob(t, db, 2)
	freshTime := fixedTime().Add(-2 * time.Hour)
	if err := repo.UpdateStatus(ctx, fresh.ID, domain.JobCompleted, &freshTime); err != nil {
		t.Fatal(err)
	}
	// The failing check constraint needs no result artifact for completed; the
	// UpdateStatus path is fine, but tasks stay pending — irrelevant here.

	list, err := repo.ListCompletedBefore(ctx, fixedTime().Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != old.ID {
		t.Errorf("list = %d jobs, want only the old one", len(list))
	}
	if err := repo.Delete(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM jobs WHERE id = ?", old.ID.String()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("job row must be gone after Delete")
	}
	// Tasks cascaded away with the job.
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE job_id = ?", old.ID.String()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("tasks must cascade with the job")
	}
}
