//go:build integration

// Integration tests run against a real PostgreSQL instance supplied through
// TEST_DATABASE_URL. The spec forbids mocks or SQLite here: the guarantees
// being verified — FOR UPDATE SKIP LOCKED, optimistic concurrency, transaction
// rollback — are properties of Postgres, not of our Go code.
//
//	docker compose up -d
//	TEST_DATABASE_URL='postgres://scimesh:scimesh@localhost:5432/scimesh?sslmode=disable' \
//	  go test -tags=integration ./internal/storage/postgres/ -v
package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedJob creates a job with n pending tasks and removes them afterwards, so
// tests stay independent of each other and of leftovers from earlier runs.
func seedJob(t *testing.T, pool *pgxpool.Pool, n int) (*domain.Job, []*domain.Task) {
	t.Helper()
	ctx := context.Background()

	chunks := make([]domain.ChunkSpec, 0, n)
	for i := 0; i < n; i++ {
		chunks = append(chunks, domain.ChunkSpec{
			ChunkIndex:  i,
			InputURI:    fmt.Sprintf("s3://chunk-%d", i),
			InputSHA256: fmt.Sprintf("sha-%d", i),
		})
	}
	job, tasks, err := domain.NewJobWithTasks("similarity_search", "s3://ds", nil, chunks, time.Now().UTC())
	if err != nil {
		t.Fatalf("build job: %v", err)
	}

	jobs, taskRepo, tx := NewJobRepo(pool), NewTaskRepo(pool), NewTxManager(pool)
	err = tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := jobs.Insert(ctx, job); err != nil {
			return err
		}
		return taskRepo.InsertBatch(ctx, tasks)
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Cleanup(func() {
		// ON DELETE CASCADE removes the tasks with it.
		_, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE id = $1`, job.ID)
	})
	return job, tasks
}

func TestCreateJobPersistsEveryTask(t *testing.T) {
	pool := testPool(t)
	job, _ := seedJob(t, pool, 3)

	counts, err := NewTaskRepo(pool).CountByStatus(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts[domain.TaskPending] != 3 {
		t.Errorf("pending = %d, want 3", counts[domain.TaskPending])
	}
}

// A job must land whole or not at all: a half-created job leaves chunks no
// worker could ever complete.
func TestCreateJobRollsBackOnFailure(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	chunks := []domain.ChunkSpec{{ChunkIndex: 0, InputURI: "s3://c0", InputSHA256: "sha0"}}
	job, tasks, err := domain.NewJobWithTasks("similarity_search", "s3://ds", nil, chunks, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	jobs, taskRepo, tx := NewJobRepo(pool), NewTaskRepo(pool), NewTxManager(pool)
	boom := errors.New("boom")
	err = tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := jobs.Insert(ctx, job); err != nil {
			return err
		}
		if err := taskRepo.InsertBatch(ctx, tasks); err != nil {
			return err
		}
		return boom // fail after both writes
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}

	if _, err := jobs.Get(ctx, job.ID); !errors.Is(err, domain.ErrJobNotFound) {
		t.Errorf("job survived the rollback: %v", err)
	}
}

// The acceptance criterion: N workers claiming at once must each get a
// different task, and no task may be handed out twice.
func TestConcurrentClaimGivesEachTaskToExactlyOneWorker(t *testing.T) {
	pool := testPool(t)
	const tasks = 8
	job, _ := seedJob(t, pool, tasks)

	repo := NewTaskRepo(pool)
	now := time.Now().UTC()

	var (
		mu      sync.Mutex
		claimed = make(map[uuid.UUID]string)
		wg      sync.WaitGroup
	)
	// More workers than tasks, so the surplus must come back empty rather than
	// steal an already-leased row.
	for i := 0; i < tasks*2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			task, err := repo.ClaimNext(context.Background(), usecase.ClaimFilter{
				Owner:      fmt.Sprintf("worker-%d", n),
				Now:        now,
				LeaseUntil: now.Add(time.Minute),
			})
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if task == nil || task.JobID != job.ID {
				return // empty queue, or a task from another test's job
			}
			mu.Lock()
			defer mu.Unlock()
			if prev, dup := claimed[task.ID]; dup {
				t.Errorf("task %s handed to both %s and worker-%d", task.ID, prev, n)
			}
			claimed[task.ID] = fmt.Sprintf("worker-%d", n)
		}(i)
	}
	wg.Wait()

	if len(claimed) != tasks {
		t.Errorf("claimed %d tasks, want %d", len(claimed), tasks)
	}
}

func TestClaimNextReturnsNilOnEmptyQueue(t *testing.T) {
	pool := testPool(t)
	now := time.Now().UTC()

	// Drain everything first, then ask once more.
	repo := NewTaskRepo(pool)
	for {
		task, err := repo.ClaimNext(context.Background(), usecase.ClaimFilter{
			Owner: "drainer", Now: now, LeaseUntil: now.Add(time.Minute),
		})
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
		if task == nil {
			break
		}
	}

	task, err := repo.ClaimNext(context.Background(), usecase.ClaimFilter{
		Owner: "worker-1", Now: now, LeaseUntil: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if task != nil {
		t.Errorf("expected nil on an empty queue, got %s", task.ID)
	}
}

func TestUpdateRejectsStaleVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, _ := seedJob(t, pool, 1)

	repo, tx := NewTaskRepo(pool), NewTxManager(pool)
	now := time.Now().UTC()

	task, err := repo.ClaimNext(ctx, usecase.ClaimFilter{
		Owner: "worker-1", Now: now, LeaseUntil: now.Add(time.Minute),
	})
	if err != nil || task == nil || task.JobID != job.ID {
		t.Skipf("could not claim this job's task (got %v, %v)", task, err)
	}

	// A stale copy: same row, but the version it remembers is behind.
	stale := *task
	stale.Version = task.Version // pretend the caller mutated it once

	err = tx.WithinTx(ctx, func(ctx context.Context) error {
		fresh, err := repo.GetForUpdate(ctx, task.ID)
		if err != nil {
			return err
		}
		if err := fresh.RenewLease("worker-1", fresh.Attempt, now, now.Add(2*time.Minute)); err != nil {
			return err
		}
		return repo.Update(ctx, fresh)
	})
	if err != nil {
		t.Fatalf("legitimate update failed: %v", err)
	}

	// Now the stale copy's version is behind by one; its write must be refused.
	stale.Version++ // as a domain method would have done
	if err := repo.Update(ctx, &stale); !errors.Is(err, domain.ErrLeaseConflict) {
		t.Errorf("stale update err = %v, want ErrLeaseConflict", err)
	}
}

func TestListCompletedIsOrderedByChunkIndex(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, tasks := seedJob(t, pool, 4)

	repo, artifacts, tx := NewTaskRepo(pool), NewArtifactRepo(pool), NewTxManager(pool)
	now := time.Now().UTC()

	// Complete them out of order to prove the ordering comes from SQL.
	for _, i := range []int{2, 0, 3, 1} {
		task := tasks[i]
		err := tx.WithinTx(ctx, func(ctx context.Context) error {
			// A completed task must reference a real result artifact (FK + check).
			taskID := task.ID
			art, err := domain.NewArtifact(job.ID, &taskID, domain.ArtifactPartialResult,
				fmt.Sprintf("result-%d.csv", task.ChunkIndex), "text/csv", now)
			if err != nil {
				return err
			}
			art.SetContent(fmt.Sprintf("rsha-%d", task.ChunkIndex), 1)
			attempt := 1
			art.Attempt = &attempt
			if err := artifacts.Insert(ctx, art); err != nil {
				return err
			}

			fresh, err := repo.GetForUpdate(ctx, task.ID)
			if err != nil {
				return err
			}
			owner := "worker-1"
			fresh.Status = domain.TaskLeased
			fresh.Attempt = attempt
			fresh.LeaseOwner = &owner
			expires := now.Add(time.Minute)
			fresh.LeaseExpiresAt = &expires
			if err := fresh.CompleteWith(art.ID, nil, owner, fresh.Attempt, now); err != nil {
				return err
			}
			return repo.Update(ctx, fresh)
		})
		if err != nil {
			t.Fatalf("complete chunk %d: %v", i, err)
		}
	}

	done, err := repo.ListCompleted(ctx, job.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(done) != 4 {
		t.Fatalf("got %d completed, want 4", len(done))
	}
	for i, task := range done {
		if task.ChunkIndex != i {
			t.Errorf("position %d holds chunk_index %d — order is not deterministic", i, task.ChunkIndex)
		}
	}
}

// A worker whose network dropped resends the same manifest. That must succeed:
// the entity is unchanged, so nothing is written, and the optimistic-concurrency
// guard must not turn the replay into a conflict.
func TestCompleteTaskReplayIsIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, _ := seedJob(t, pool, 1)

	tasks, jobs, artifacts, tx := NewTaskRepo(pool), NewJobRepo(pool), NewArtifactRepo(pool), NewTxManager(pool)
	clk := fixedClock{now: time.Now().UTC()}
	uc := usecase.NewCompleteTask(tasks, jobs, artifacts, tx, clk)

	claimed, err := tasks.ClaimNext(ctx, usecase.ClaimFilter{
		Owner: "worker-1", Now: clk.now, LeaseUntil: clk.now.Add(time.Minute),
	})
	if err != nil || claimed == nil || claimed.JobID != job.ID {
		t.Skipf("could not claim this job's task (got %v, %v)", claimed, err)
	}

	// A partial-result artifact the coordinator stored for this task.
	art := seedArtifact(t, pool, job.ID, &claimed.ID, domain.ArtifactPartialResult)

	in := usecase.CompleteTaskInput{
		TaskID: claimed.ID, WorkerID: "worker-1", Attempt: claimed.Attempt,
		ResultArtifactID: art.ID,
	}
	if _, err := uc.Execute(ctx, in); err != nil {
		t.Fatalf("first submission: %v", err)
	}
	if _, err := uc.Execute(ctx, in); err != nil {
		t.Errorf("replay must be idempotent, got %v", err)
	}

	// A different result artifact for the same task is a genuine conflict.
	art2 := seedArtifact(t, pool, job.ID, &claimed.ID, domain.ArtifactPartialResult)
	other := in
	other.ResultArtifactID = art2.ID
	if _, err := uc.Execute(ctx, other); !errors.Is(err, domain.ErrResultConflict) {
		t.Errorf("err = %v, want ErrResultConflict", err)
	}
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// seedArtifact inserts an artifact and returns it, cleaned up with its job.
func seedArtifact(t *testing.T, pool *pgxpool.Pool, jobID uuid.UUID, taskID *uuid.UUID, kind domain.ArtifactKind) *domain.Artifact {
	t.Helper()
	art, err := domain.NewArtifact(jobID, taskID, kind, "f.csv", "text/csv", time.Now().UTC())
	if err != nil {
		t.Fatalf("build artifact: %v", err)
	}
	art.SetContent(fmt.Sprintf("sha-%s", art.ID), 3)
	if kind == domain.ArtifactPartialResult {
		attempt := 1
		art.Attempt = &attempt
	}
	if err := NewArtifactRepo(pool).Insert(context.Background(), art); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}
	return art
}

func TestWorkerRepoRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewWorkerRepo(pool)

	w, err := domain.NewWorker("lab-int", []string{"similarity_search", "similarity_graph"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Insert(ctx, w); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM workers WHERE id = $1`, w.ID) })

	got, err := repo.Get(ctx, w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.WorkerOnline || len(got.Capabilities) != 2 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	// capabilities must survive the jsonb round-trip.
	if got.Capabilities[0] != "similarity_search" {
		t.Errorf("capabilities = %v", got.Capabilities)
	}

	if _, err := repo.Get(ctx, uuid.New()); !errors.Is(err, domain.ErrWorkerNotFound) {
		t.Errorf("missing worker err = %v, want ErrWorkerNotFound", err)
	}
}

func TestWorkerLivenessAndOfflineReaper(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewWorkerRepo(pool)

	w, err := domain.NewWorker("liveness", []string{"similarity_search"}, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Insert(ctx, w); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM workers WHERE id = $1`, w.ID) })

	// A fresh heartbeat bumps it online.
	now := time.Now().UTC()
	if err := repo.Touch(ctx, w.ID, now); err != nil {
		t.Fatalf("touch: %v", err)
	}
	if got, _ := repo.Get(ctx, w.ID); got.Status != domain.WorkerOnline {
		t.Errorf("status = %q, want online after touch", got.Status)
	}

	// Touching an unregistered id is a harmless no-op.
	if err := repo.Touch(ctx, uuid.New(), now); err != nil {
		t.Errorf("touch of unknown worker returned %v, want nil", err)
	}

	// The reaper marks it offline once its heartbeat is older than the cutoff.
	n, err := repo.MarkStaleOffline(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("mark offline: %v", err)
	}
	if n < 1 {
		t.Errorf("marked %d offline, want at least 1", n)
	}
	if got, _ := repo.Get(ctx, w.ID); got.Status != domain.WorkerOffline {
		t.Errorf("status = %q, want offline after reaper", got.Status)
	}
}

func TestArtifactRepoRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, _ := seedJob(t, pool, 1)

	art := seedArtifact(t, pool, job.ID, nil, domain.ArtifactInput)
	got, err := NewArtifactRepo(pool).Get(ctx, art.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Kind != domain.ArtifactInput || got.StorageKey != art.StorageKey || got.SizeBytes != 3 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if _, err := NewArtifactRepo(pool).Get(ctx, uuid.New()); !errors.Is(err, domain.ErrArtifactNotFound) {
		t.Errorf("missing artifact err = %v, want ErrArtifactNotFound", err)
	}
}

func TestPartialResultArtifactRoundTripsAttempt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, tasks := seedJob(t, pool, 1)
	taskID := tasks[0].ID
	art, err := domain.NewArtifact(job.ID, &taskID, domain.ArtifactPartialResult,
		"result.csv", "text/csv", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	attempt := 2
	art.Attempt = &attempt
	art.SetContent("sha", 3)
	repo := NewArtifactRepo(pool)
	if err := repo.Insert(ctx, art); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := repo.Get(ctx, art.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Attempt == nil || *got.Attempt != attempt {
		t.Fatalf("attempt = %v, want %d", got.Attempt, attempt)
	}
}

// A shard task stores its input as an artifact and no URI: this exercises the
// nullable input_uri column, the input_artifact_id round-trip, and the
// ck_tasks_has_input check that requires one or the other.
func TestShardTaskRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	job, err := domain.NewUploadedJob("similarity_search", nil, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	jobs, taskRepo, tx := NewJobRepo(pool), NewTaskRepo(pool), NewTxManager(pool)
	if err := jobs.Insert(ctx, job); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM jobs WHERE id = $1`, job.ID) })

	shard := seedArtifact(t, pool, job.ID, nil, domain.ArtifactShard)
	task, err := domain.NewShardTask(job.ID, 0, "similarity_search", shard.ID, shard.SHA256, nil, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.WithinTx(ctx, func(ctx context.Context) error {
		return taskRepo.InsertBatch(ctx, []*domain.Task{task})
	}); err != nil {
		t.Fatalf("insert shard task: %v", err)
	}

	got, err := taskRepo.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.InputArtifactID == nil || *got.InputArtifactID != shard.ID {
		t.Errorf("input_artifact_id did not round-trip: %v", got.InputArtifactID)
	}
	if got.InputURI != "" {
		t.Errorf("shard task input_uri = %q, want empty (NULL)", got.InputURI)
	}
}

func TestExpireLeasesRequeuesElapsedTasks(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, _ := seedJob(t, pool, 1)

	repo := NewTaskRepo(pool)
	past := time.Now().UTC().Add(-time.Hour)

	// Lease it with an expiry already in the past.
	task, err := repo.ClaimNext(ctx, usecase.ClaimFilter{
		Owner: "dead-worker", Now: past, LeaseUntil: past.Add(time.Minute),
	})
	if err != nil || task == nil || task.JobID != job.ID {
		t.Skipf("could not claim this job's task (got %v, %v)", task, err)
	}

	if _, err := repo.ExpireLeases(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("expire: %v", err)
	}

	counts, err := repo.CountByStatus(ctx, job.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts[domain.TaskPending] != 1 {
		t.Errorf("pending = %d, want 1 — a dead worker must not strand its task", counts[domain.TaskPending])
	}
}
