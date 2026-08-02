package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// newTestDB opens an isolated on-disk database and applies the migrations.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(context.Background(), db, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func fixedTime() time.Time {
	return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
}

func seedJob(t *testing.T, db *sql.DB, n int) *domain.Job {
	t.Helper()
	ctx := context.Background()
	tx := NewTxManager(db)
	chunks := make([]domain.ChunkSpec, 0, n)
	for i := 0; i < n; i++ {
		chunks = append(chunks, domain.ChunkSpec{
			ChunkIndex:  i,
			InputURI:    "s3://chunk-" + string(rune('a'+i)),
			InputSHA256: "sha-" + string(rune('a'+i)),
		})
	}
	job, tasks, err := domain.NewJobWithTasks("similarity_search", "s3://ds", nil, chunks, fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := NewJobRepo(db).Insert(ctx, job); err != nil {
			return err
		}
		return NewTaskRepo(db).InsertBatch(ctx, tasks)
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return job
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := Migrate(ctx, db, nil); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Errorf("user_version = %d, want 1", version)
	}
}

func TestJobRepoRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	job := seedJob(t, db, 1)

	got, err := NewJobRepo(db).Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Workload != job.Workload || got.Status != domain.JobPending {
		t.Errorf("job = %+v", got)
	}
	if err := NewJobRepo(db).UpdateStatus(ctx, job.ID, domain.JobRunning, nil); err != nil {
		t.Fatal(err)
	}
	got, err = NewJobRepo(db).Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.JobRunning {
		t.Errorf("status = %q, want running", got.Status)
	}
	if _, err := NewJobRepo(db).Get(ctx, uuid.New()); !errors.Is(err, domain.ErrJobNotFound) {
		t.Errorf("missing job err = %v, want ErrJobNotFound", err)
	}
}

func TestClaimGivesEachTaskToExactlyOneWorker(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedJob(t, db, 3)
	repo := NewTaskRepo(db)

	claimed := map[uuid.UUID]bool{}
	for i := 0; i < 3; i++ {
		task, err := repo.ClaimNext(ctx, usecase.ClaimFilter{
			Workloads:  []string{"similarity_search"},
			Owner:      "w1",
			Now:        fixedTime(),
			LeaseUntil: fixedTime().Add(time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		if task == nil {
			t.Fatal("claim returned nil on a non-empty queue")
		}
		if claimed[task.ID] {
			t.Fatalf("task %s claimed twice", task.ID)
		}
		claimed[task.ID] = true
		if task.Status != domain.TaskLeased || task.Attempt != 1 || task.LeaseOwner == nil || *task.LeaseOwner != "w1" {
			t.Errorf("task = %+v", task)
		}
	}
	task, err := repo.ClaimNext(ctx, usecase.ClaimFilter{Workloads: []string{"similarity_search"}, Owner: "w1", Now: fixedTime(), LeaseUntil: fixedTime().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if task != nil {
		t.Fatal("claim must return nil on an empty queue")
	}
}

func TestUpdateRejectsStaleVersion(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedJob(t, db, 1)
	repo := NewTaskRepo(db)
	task, err := repo.ClaimNext(ctx, usecase.ClaimFilter{Workloads: nil, Owner: "w1", Now: fixedTime(), LeaseUntil: fixedTime().Add(time.Minute)})
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	stale := *task
	task.Status = domain.TaskRunning
	task.Version++ // as a domain method would have done
	if err := repo.Update(ctx, task); err != nil {
		t.Fatal(err)
	}
	stale.Status = domain.TaskCompleted
	stale.Version++
	if err := repo.Update(ctx, &stale); !errors.Is(err, domain.ErrLeaseConflict) {
		t.Errorf("stale update err = %v, want ErrLeaseConflict", err)
	}
}

func TestExpireLeasesRequeuesElapsedTasks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedJob(t, db, 1)
	repo := NewTaskRepo(db)
	task, err := repo.ClaimNext(ctx, usecase.ClaimFilter{Workloads: nil, Owner: "w1", Now: fixedTime(), LeaseUntil: fixedTime().Add(-time.Minute)})
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	affected, err := repo.ExpireLeases(ctx, fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if len(affected) != 1 {
		t.Fatalf("affected = %v, want 1 job", affected)
	}
	task, err = repo.ClaimNext(ctx, usecase.ClaimFilter{Workloads: nil, Owner: "w2", Now: fixedTime(), LeaseUntil: fixedTime().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if task == nil || task.Attempt != 2 {
		t.Errorf("requeued task = %+v, want attempt 2", task)
	}
}

func TestExpireLeasesFailsAfterFinalAttempt(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedJob(t, db, 1)
	repo := NewTaskRepo(db)
	for attempt := 1; attempt <= 3; attempt++ {
		task, err := repo.ClaimNext(ctx, usecase.ClaimFilter{Workloads: nil, Owner: "w1", Now: fixedTime(), LeaseUntil: fixedTime().Add(-time.Minute)})
		if err != nil || task == nil {
			t.Fatalf("claim %d: %v", attempt, err)
		}
		if _, err := repo.ExpireLeases(ctx, fixedTime()); err != nil {
			t.Fatal(err)
		}
	}
	task, err := repo.ClaimNext(ctx, usecase.ClaimFilter{Workloads: nil, Owner: "w1", Now: fixedTime(), LeaseUntil: fixedTime().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if task != nil {
		t.Fatal("exhausted task must not be claimable")
	}
}

func TestArtifactRepoRoundTripAndUniqueness(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	job := seedJob(t, db, 1)
	repo := NewArtifactRepo(db)
	attempt := 1
	artifact, err := domain.NewArtifact(job.ID, nil, domain.ArtifactShard, "shard-0.tsv", "text/tab-separated-values", fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	artifact.SetContent("abc123", 42)
	if err := repo.Insert(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != "abc123" || got.SizeBytes != 42 {
		t.Errorf("artifact = %+v", got)
	}

	partialTask := uuid.New()
	partial, err := domain.NewArtifact(job.ID, &partialTask, domain.ArtifactPartialResult, "p.csv", "text/csv", fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	partial.Attempt = &attempt
	if err := repo.Insert(ctx, partial); err != nil {
		t.Fatal(err)
	}
	found, err := repo.FindPartialResult(ctx, partialTask, attempt)
	if err != nil || found == nil {
		t.Fatalf("find partial: %v", err)
	}
	duplicate, err := domain.NewArtifact(job.ID, &partialTask, domain.ArtifactPartialResult, "p2.csv", "text/csv", fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	duplicate.Attempt = &attempt
	if err := repo.Insert(ctx, duplicate); err == nil {
		t.Fatal("duplicate partial for the same attempt must fail")
	}
}

func TestWorkerRepoRoundTripAndLiveness(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := NewWorkerRepo(db)
	worker, err := domain.NewWorker("w1", []string{"similarity-search"}, fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Insert(ctx, worker); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "w1" || len(got.Capabilities) != 1 || got.TrustLevel != domain.WorkerTrusted {
		t.Errorf("worker = %+v", got)
	}
	if err := repo.Touch(ctx, worker.ID, fixedTime().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	changed, err := repo.MarkStaleOffline(ctx, fixedTime().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Errorf("offline changes = %d, want 1", changed)
	}
	got, err = repo.Get(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.WorkerOffline {
		t.Errorf("status = %q, want offline", got.Status)
	}
}

func TestTaskResultVotes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	job := seedJob(t, db, 1)
	repo := NewTaskResultRepo(db)
	task, err := domain.NewTask(job.ID, 1, "similarity_search", "s3://in", "sha", nil, 3, fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if err := NewTaskRepo(db).InsertBatch(ctx, []*domain.Task{task}); err != nil {
		t.Fatal(err)
	}
	taskID := task.ID
	artifactID := uuid.New()
	ownerA, ownerB := uuid.New(), uuid.New()
	if err := repo.RecordVote(ctx, taskID, ownerA, "hash", artifactID); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordVote(ctx, taskID, ownerB, "hash", artifactID); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordVote(ctx, taskID, ownerA, "hash2", artifactID); err != nil {
		t.Fatal(err)
	}
	n, err := repo.CountAgreeing(ctx, taskID, "hash")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("agreeing = %d, want 1 (owner A changed its vote)", n)
	}
}

func TestCancelByJobInvalidatesTasks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	job := seedJob(t, db, 2)
	repo := NewTaskRepo(db)
	task, err := repo.ClaimNext(ctx, usecase.ClaimFilter{Workloads: nil, Owner: "w1", Now: fixedTime(), LeaseUntil: fixedTime().Add(time.Minute)})
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	cancelled, err := repo.CancelByJob(ctx, job.ID, fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if cancelled != 2 {
		t.Errorf("cancelled = %d, want 2", cancelled)
	}
	got, err := repo.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskCancelled || got.LeaseOwner != nil {
		t.Errorf("cancelled task = %+v", got)
	}
}
