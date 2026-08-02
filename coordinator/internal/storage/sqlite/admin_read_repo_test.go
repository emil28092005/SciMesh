package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

func TestAdminListJobsPaginatedAndCounts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	jobRepo := NewJobRepo(db)
	adminRepo := NewAdminReadRepo(db)

	jobs := make([]*domain.Job, 5)
	for i := range jobs {
		jobs[i] = seedJob(t, db, 2)
	}
	// Two completed, two running, one pending.
	if err := jobRepo.UpdateStatus(ctx, jobs[0].ID, domain.JobCompleted, nil); err != nil {
		t.Fatal(err)
	}
	if err := jobRepo.UpdateStatus(ctx, jobs[1].ID, domain.JobCompleted, nil); err != nil {
		t.Fatal(err)
	}
	if err := jobRepo.UpdateStatus(ctx, jobs[2].ID, domain.JobRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := jobRepo.UpdateStatus(ctx, jobs[3].ID, domain.JobRunning, nil); err != nil {
		t.Fatal(err)
	}

	all, total, err := adminRepo.ListJobsPaginated(ctx, "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(all) != 5 {
		t.Errorf("all: total=%d len=%d, want 5/5", total, len(all))
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
	if total != 5 || len(page) != 2 {
		t.Errorf("page: total=%d len=%d, want 5/2", total, len(page))
	}

	counts, err := adminRepo.CountJobsByStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["completed"] != 2 || counts["running"] != 2 || counts["pending"] != 1 {
		t.Errorf("counts = %v, want completed=2 running=2 pending=1", counts)
	}
}

func TestAdminTaskCountsByJobs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	job := seedJob(t, db, 3)
	if _, err := db.ExecContext(ctx, "UPDATE tasks SET status = 'completed', result_artifact_id = ? WHERE chunk_index = 0 AND job_id = ?", uuid.NewString(), job.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE tasks SET status = 'failed' WHERE chunk_index = 1 AND job_id = ?", job.ID.String()); err != nil {
		t.Fatal(err)
	}

	counts, err := NewAdminReadRepo(db).TaskCountsByJobs(ctx, []uuid.UUID{job.ID})
	if err != nil {
		t.Fatal(err)
	}
	got := counts[job.ID]
	if got["completed"] != 1 || got["failed"] != 1 || got["pending"] != 1 {
		t.Errorf("task counts = %v, want completed=1 failed=1 pending=1", got)
	}
}

func TestAdminJobCountsByDay(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	job := seedJob(t, db, 1)
	// Move the seed job to two days ago; create two more today.
	old := fixedTime().Add(-48 * time.Hour)
	if _, err := db.ExecContext(ctx, "UPDATE jobs SET created_at = ? WHERE id = ?", old.UnixNano(), job.ID.String()); err != nil {
		t.Fatal(err)
	}
	seedJob(t, db, 1)
	seedJob(t, db, 1)

	repo := NewAdminReadRepo(db)
	counts, err := repo.JobCountsByDay(ctx, fixedTime().Add(-6*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	today := fixedTime().UTC().Format("2006-01-02")
	oldDay := old.UTC().Format("2006-01-02")
	if counts[today] != 2 {
		t.Errorf("today count = %d, want 2 (got %v)", counts[today], counts)
	}
	if counts[oldDay] != 1 {
		t.Errorf("old day count = %d, want 1 (got %v)", counts[oldDay], counts)
	}
}

func TestAdminTaskStatsAndStorage(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := NewAdminReadRepo(db)

	// One completed task with a known duration, one failed.
	job := seedJob(t, db, 2)
	start := fixedTime().Add(-2 * time.Minute)
	done := fixedTime().Add(-90 * time.Second)
	queries := []string{
		"UPDATE tasks SET status='completed', result_artifact_id=?, started_at=?, completed_at=? WHERE job_id=? AND chunk_index=0",
		"UPDATE tasks SET status='failed' WHERE job_id=? AND chunk_index=1",
	}
	for i, q := range queries {
		args := []any{uuid.NewString(), start.UnixNano(), done.UnixNano(), job.ID.String()}
		if i == 1 {
			args = []any{job.ID.String()}
		}
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}

	completed, failed, avg, err := repo.TaskStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if completed != 1 || failed != 1 {
		t.Errorf("stats = completed %d failed %d, want 1/1", completed, failed)
	}
	if avg < 29 || avg > 31 {
		t.Errorf("avg duration = %.1fs, want ~30s", avg)
	}

	// Artifact sizes by kind.
	for _, kind := range []string{"input", "shard", "final_result"} {
		if _, err := db.ExecContext(ctx, "INSERT INTO artifacts (id, job_id, kind, filename, storage_key, content_type, size_bytes, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			uuid.NewString(), job.ID.String(), kind, kind+".csv", "key-"+kind, "text/csv", int64(len(kind)*1000), fixedTime().UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	sizes, err := repo.ArtifactSizeByKind(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sizes["input"] != 5000 || sizes["shard"] != 5000 || sizes["final_result"] != 12000 {
		t.Errorf("sizes = %v", sizes)
	}
	dbBytes, err := repo.DatabaseSizeBytes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dbBytes <= 0 {
		t.Errorf("database size = %d, want > 0", dbBytes)
	}
}
