//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// ensureMigrated applies the embedded schema first: the admin tests run
// before the dedicated migration test (file order) and need real tables.
func ensureMigrated(t *testing.T) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	if err := Migrate(context.Background(), url, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func TestAdminWorkerSetTrust(t *testing.T) {
	ensureMigrated(t)
	pool := testPool(t)
	ctx := context.Background()
	repo := NewWorkerRepo(pool)

	w, err := domain.NewWorker("trust-lab", []string{"similarity-search"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Insert(ctx, w); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM workers WHERE id = $1`, w.ID) })

	if err := repo.SetTrust(ctx, w.ID, domain.WorkerUntrusted); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrustLevel != domain.WorkerUntrusted {
		t.Errorf("trust = %q, want untrusted", got.TrustLevel)
	}
	if err := repo.SetTrust(ctx, uuid.New(), domain.WorkerTrusted); !errors.Is(err, domain.ErrWorkerNotFound) {
		t.Errorf("unknown worker: got %v, want ErrWorkerNotFound", err)
	}
}

func TestAdminListJobsPaginatedAndCounts(t *testing.T) {
	ensureMigrated(t)
	pool := testPool(t)
	ctx := context.Background()
	jobRepo := NewJobRepo(pool)
	adminRepo := NewAdminReadRepo(pool)

	jobs := make([]*domain.Job, 4)
	for i := range jobs {
		job, _ := seedJob(t, pool, 1)
		jobs[i] = job
	}
	for _, j := range jobs {
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE id = $1`, j.ID) })
	}
	if err := jobRepo.UpdateStatus(ctx, jobs[0].ID, domain.JobCompleted, nil); err != nil {
		t.Fatal(err)
	}
	if err := jobRepo.UpdateStatus(ctx, jobs[1].ID, domain.JobCompleted, nil); err != nil {
		t.Fatal(err)
	}
	if err := jobRepo.UpdateStatus(ctx, jobs[2].ID, domain.JobRunning, nil); err != nil {
		t.Fatal(err)
	}

	all, total, err := adminRepo.ListJobsPaginated(ctx, "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(all) != 4 {
		t.Errorf("all: total=%d len=%d, want 4/4", total, len(all))
	}
	completed, total, err := adminRepo.ListJobsPaginated(ctx, "completed", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(completed) != 2 {
		t.Errorf("completed: total=%d len=%d, want 2/2", total, len(completed))
	}
	page, total, err := adminRepo.ListJobsPaginated(ctx, "", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(page) != 2 {
		t.Errorf("page: total=%d len=%d, want 4/2", total, len(page))
	}

	counts, err := adminRepo.CountJobsByStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["completed"] != 2 || counts["running"] != 1 || counts["pending"] != 1 {
		t.Errorf("counts = %v", counts)
	}
}

func TestAdminTaskCountsByJobs(t *testing.T) {
	ensureMigrated(t)
	pool := testPool(t)
	ctx := context.Background()
	job, tasks := seedJob(t, pool, 3)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE id = $1`, job.ID) })

	resultArtifact := seedArtifact(t, pool, job.ID, &tasks[0].ID, domain.ArtifactPartialResult)
	if _, err := pool.Exec(ctx, `UPDATE tasks SET status = 'completed', result_artifact_id = $1 WHERE id = $2`,
		resultArtifact.ID, tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tasks SET status = 'failed' WHERE id = $1`, tasks[1].ID); err != nil {
		t.Fatal(err)
	}

	counts, err := NewAdminReadRepo(pool).TaskCountsByJobs(ctx, []uuid.UUID{job.ID})
	if err != nil {
		t.Fatal(err)
	}
	got := counts[job.ID]
	if got["completed"] != 1 || got["failed"] != 1 || got["pending"] != 1 {
		t.Errorf("task counts = %v, want completed=1 failed=1 pending=1", got)
	}
}

func TestAdminJobCountsByDayAndWorkload(t *testing.T) {
	ensureMigrated(t)
	pool := testPool(t)
	ctx := context.Background()
	repo := NewAdminReadRepo(pool)

	job, _ := seedJob(t, pool, 1)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE id = $1`, job.ID) })
	if _, err := pool.Exec(ctx, `UPDATE jobs SET created_at = now() - interval '2 days' WHERE id = $1`, job.ID); err != nil {
		t.Fatal(err)
	}
	other, _ := seedJob(t, pool, 1)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE id = $1`, other.ID) })

	byDay, err := repo.JobCountsByDay(ctx, time.Now().UTC().Add(-6*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	twoDays := time.Now().UTC().Add(-48 * time.Hour).Format("2006-01-02")
	if byDay[today] != 1 || byDay[twoDays] != 1 {
		t.Errorf("by day = %v, want today=1 twoDays=1", byDay)
	}

	byWorkload, err := repo.JobCountsByWorkload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if byWorkload[job.Workload] < 2 {
		t.Errorf("by workload = %v, want at least 2 for %s", byWorkload, job.Workload)
	}
}

func TestAdminTaskStatsAndStorage(t *testing.T) {
	ensureMigrated(t)
	pool := testPool(t)
	ctx := context.Background()
	repo := NewAdminReadRepo(pool)

	job, tasks := seedJob(t, pool, 2)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE id = $1`, job.ID) })

	start := time.Now().UTC().Add(-2 * time.Minute)
	done := time.Now().UTC().Add(-90 * time.Second)
	resultArtifact := seedArtifact(t, pool, job.ID, &tasks[0].ID, domain.ArtifactPartialResult)
	if _, err := pool.Exec(ctx, `UPDATE tasks SET status = 'completed', result_artifact_id = $1, started_at = $2, completed_at = $3 WHERE id = $4`,
		resultArtifact.ID, start, done, tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tasks SET status = 'failed' WHERE id = $1`, tasks[1].ID); err != nil {
		t.Fatal(err)
	}

	completed, failed, avg, err := repo.TaskStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if completed < 1 || failed < 1 {
		t.Errorf("stats = completed %d failed %d, want >= 1/1", completed, failed)
	}
	if avg < 29 || avg > 31 {
		t.Errorf("avg duration = %.1fs, want ~30s", avg)
	}

	for _, kind := range []domain.ArtifactKind{domain.ArtifactInput, domain.ArtifactShard} {
		seedArtifact(t, pool, job.ID, nil, kind)
	}
	sizes, err := repo.ArtifactSizeByKind(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sizes["input"] < 1 || sizes["shard"] < 1 {
		t.Errorf("sizes = %v, want input/shard > 0", sizes)
	}
	dbBytes, err := repo.DatabaseSizeBytes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dbBytes <= 0 {
		t.Errorf("database size = %d, want > 0", dbBytes)
	}
}

func TestWorkloadSettingsRepoRoundTrip(t *testing.T) {
	ensureMigrated(t)
	pool := testPool(t)
	ctx := context.Background()
	repo := NewWorkloadSettingsRepo(pool)

	enabled, err := repo.GetEnabled(ctx, "similarity-search")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Error("workload without an override must be enabled")
	}
	now := time.Now().UTC()
	if err := repo.SetEnabled(ctx, "similarity-search", false, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM workload_settings WHERE workload = 'similarity-search'`)
	})
	enabled, err = repo.GetEnabled(ctx, "similarity-search")
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Error("workload must be disabled after the override")
	}
	if err := repo.SetEnabled(ctx, "similarity-search", true, now.Add(time.Hour)); err != nil {
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
	found := false
	for _, s := range list {
		if s.Workload == "similarity-search" {
			found = true
			if !s.Enabled {
				t.Error("list must reflect the re-enabled state")
			}
		}
	}
	if !found {
		t.Error("override missing from the settings list")
	}
}
