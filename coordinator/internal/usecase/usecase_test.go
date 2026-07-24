package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/memstore"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

var ctx = context.Background()

const lease = 2 * time.Minute

type expiringBlobStore struct {
	*memstore.BlobStore
	clock *memstore.Clock
}

func (s expiringBlobStore) Put(ctx context.Context, key string, body io.Reader) (string, int64, error) {
	sum, size, err := s.BlobStore.Put(ctx, key, body)
	s.clock.Advance(lease + time.Second)
	return sum, size, err
}

// harness wires every use case to in-memory stores so orchestration can be
// tested without a database.
type harness struct {
	tasks *memstore.TaskRepo
	jobs  *memstore.JobRepo
	work  *memstore.WorkerRepo
	arts  *memstore.ArtifactRepo
	blobs *memstore.BlobStore
	clk   *memstore.Clock

	createJob   *usecase.CreateJob
	submit      *usecase.SubmitDataset
	claim       *usecase.ClaimTask
	renew       *usecase.RenewLease
	complete    *usecase.CompleteTask
	fail        *usecase.FailTask
	status      *usecase.GetJobStatus
	results     *usecase.ListResults
	register    *usecase.RegisterWorker
	uploadArt   *usecase.UploadArtifact
	downloadArt *usecase.DownloadArtifact
	getInput    *usecase.GetTaskInput
	expire      *usecase.ExpireLeases
	cancel      *usecase.CancelJob
	reduce      *usecase.ReduceJob
	jobResult   *usecase.GetJobResult
}

func newHarness() *harness {
	h := &harness{
		tasks: memstore.NewTaskRepo(),
		jobs:  memstore.NewJobRepo(),
		work:  memstore.NewWorkerRepo(),
		arts:  memstore.NewArtifactRepo(),
		blobs: memstore.NewBlobStore(),
		clk:   memstore.NewClock(time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)),
	}
	tx := memstore.Tx{}
	h.createJob = usecase.NewCreateJob(h.jobs, h.tasks, tx, h.clk)
	h.submit = usecase.NewSubmitDataset(h.blobs, h.arts, h.jobs, h.tasks, tx, h.clk, 3)
	h.claim = usecase.NewClaimTask(h.tasks, h.jobs, h.work, tx, h.clk, lease)
	h.renew = usecase.NewRenewLease(h.tasks, h.work, tx, h.clk, lease)
	h.complete = usecase.NewCompleteTask(h.tasks, h.jobs, h.arts, tx, h.clk)
	h.fail = usecase.NewFailTask(h.tasks, h.jobs, tx, h.clk)
	h.status = usecase.NewGetJobStatus(h.jobs, h.tasks)
	h.results = usecase.NewListResults(h.tasks)
	h.register = usecase.NewRegisterWorker(h.work, h.clk)
	h.uploadArt = usecase.NewUploadArtifact(h.tasks, h.arts, h.blobs, tx, h.clk)
	h.downloadArt = usecase.NewDownloadArtifact(h.arts, h.blobs)
	h.getInput = usecase.NewGetTaskInput(h.tasks, h.arts, h.blobs)
	h.expire = usecase.NewExpireLeases(h.tasks, h.jobs, tx, h.clk)
	h.cancel = usecase.NewCancelJob(h.jobs, h.tasks, tx, h.clk)
	h.reduce = usecase.NewReduceJob(h.jobs, h.tasks, h.arts, h.blobs, tx, h.clk)
	h.jobResult = usecase.NewGetJobResult(h.jobs, h.downloadArt)
	return h
}

func TestSimilaritySearchReductionCreatesFinalArtifact(t *testing.T) {
	h := newHarness()
	jobID := h.seedJob(t, "similarity-search", 2)
	_, err := h.jobs.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.jobs.UpdateStatus(ctx, jobID, domain.JobRunning, nil); err != nil {
		t.Fatal(err)
	}
	partials := []string{
		"rank,chembl_id,canonical_smiles,similarity\n1,B,CCC,0.50000048\n",
		"rank,chembl_id,canonical_smiles,similarity\n1,A,CC,0.50000049\n",
	}
	for _, partial := range partials {
		taskID, attempt := h.leaseOne(t, "w1", "similarity-search")
		art, err := h.uploadArt.Execute(ctx, usecase.UploadArtifactInput{TaskID: taskID, WorkerID: "w1", Attempt: attempt, Filename: "partial.csv", ContentType: "text/csv", Body: strings.NewReader(partial)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.complete.Execute(ctx, usecase.CompleteTaskInput{TaskID: taskID, WorkerID: "w1", Attempt: attempt, ResultArtifactID: art.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.reduce.Execute(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	progress, err := h.status.Execute(ctx, jobID)
	if err != nil || progress.Job.Status != domain.JobCompleted {
		t.Fatalf("status=%s err=%v", progress.Job.Status, err)
	}
	art, body, err := h.jobResult.Execute(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	bytes, _ := io.ReadAll(body)
	if art.Kind != domain.ArtifactFinalResult || string(bytes) != "rank,chembl_id,canonical_smiles,similarity\n1,A,CC,0.500000\n2,B,CCC,0.500000\n" {
		t.Fatalf("unexpected final %q", bytes)
	}
}

func TestSimilaritySearchReductionFailureIsSanitized(t *testing.T) {
	h := newHarness()
	jobID := h.seedJob(t, "similarity-search", 1)
	if err := h.jobs.UpdateStatus(ctx, jobID, domain.JobRunning, nil); err != nil {
		t.Fatal(err)
	}
	taskID, attempt := h.leaseOne(t, "w1", "similarity-search")
	art, err := h.uploadArt.Execute(ctx, usecase.UploadArtifactInput{
		TaskID: taskID, WorkerID: "w1", Attempt: attempt, Filename: "partial.csv",
		ContentType: "text/csv", Body: strings.NewReader("rank,chembl_id,canonical_smiles,similarity\n2,A,CC,0.9\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.complete.Execute(ctx, usecase.CompleteTaskInput{
		TaskID: taskID, WorkerID: "w1", Attempt: attempt, ResultArtifactID: art.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.reduce.Execute(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	job, err := h.jobs.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != domain.JobFailed || job.ErrorCode == nil || *job.ErrorCode != "reducer_failed" ||
		job.ErrorMessage == nil || *job.ErrorMessage != "final result reduction failed" {
		t.Fatalf("unexpected failed job: %+v", job)
	}
	if job.ResultArtifactID != nil {
		t.Fatal("failed reduction must not expose a final result")
	}
}

// seedJob creates a URI-chunked job with n chunks and returns its id.
func (h *harness) seedJob(t *testing.T, workload string, n int) uuid.UUID {
	t.Helper()
	in := usecase.CreateJobInput{Workload: workload, InputURI: "s3://in"}
	for i := 0; i < n; i++ {
		in.Chunks = append(in.Chunks, usecase.ChunkInput{
			ChunkIndex: i, InputURI: fmt.Sprintf("s3://c%d", i), InputSHA256: "sha",
		})
	}
	job, err := h.createJob.Execute(ctx, in)
	if err != nil {
		t.Fatalf("seedJob: %v", err)
	}
	return job.ID
}

// leaseOne claims a single task for worker and returns its id and attempt.
func (h *harness) leaseOne(t *testing.T, worker, workload string) (uuid.UUID, int) {
	t.Helper()
	c, err := h.claim.Execute(ctx, usecase.ClaimTaskInput{WorkerID: worker, Workloads: []string{workload}})
	if err != nil || c == nil {
		t.Fatalf("leaseOne: claim returned (%v, %v)", c, err)
	}
	return c.TaskID, c.Attempt
}

// uploadResult stores a partial-result artifact for a leased task.
func (h *harness) uploadResult(t *testing.T, taskID uuid.UUID, worker string, attempt int) uuid.UUID {
	t.Helper()
	art, err := h.uploadArt.Execute(ctx, usecase.UploadArtifactInput{
		TaskID: taskID, WorkerID: worker, Attempt: attempt,
		Filename: "r.csv", ContentType: "text/csv", Body: strings.NewReader("q,m\nA,B\n"),
	})
	if err != nil {
		t.Fatalf("uploadResult: %v", err)
	}
	return art.ID
}

// --- ClaimTask -----------------------------------------------------------

func TestClaimLeasesAndAdvancesAttempt(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 1)

	c, err := h.claim.Execute(ctx, usecase.ClaimTaskInput{WorkerID: "w1", Workloads: []string{"w"}})
	if err != nil || c == nil {
		t.Fatalf("claim = (%v, %v)", c, err)
	}
	if c.Attempt != 1 || c.LeaseOwner != "w1" {
		t.Errorf("attempt=%d owner=%q, want 1/w1", c.Attempt, c.LeaseOwner)
	}
}

func TestRegisteredWorkerCannotBroadenItsCapabilitiesAtClaim(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "restricted", 1)
	worker, err := h.register.Execute(ctx, usecase.RegisterWorkerInput{
		Name: "search-only", Capabilities: []string{"similarity-search"},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := h.claim.Execute(ctx, usecase.ClaimTaskInput{
		WorkerID: worker.ID.String(), Workloads: []string{"restricted"},
	})
	if err != nil || claimed != nil {
		t.Fatalf("claim = (%v, %v), want no compatible task", claimed, err)
	}
}

func TestCreateJobRejectsUnsafeDistributedScientificPlans(t *testing.T) {
	h := newHarness()
	_, err := h.createJob.Execute(ctx, usecase.CreateJobInput{
		Workload: "similarity-graph", InputURI: "s3://input",
		Chunks: []usecase.ChunkInput{{ChunkIndex: 0, InputURI: "s3://chunk", InputSHA256: "sha"}},
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("graph job err = %v, want ErrInvalidInput", err)
	}
	_, err = h.createJob.Execute(ctx, usecase.CreateJobInput{
		Workload: "similarity-search", InputURI: "s3://input", Parameters: map[string]any{"query_id": "CHEMBL1"},
		Chunks: []usecase.ChunkInput{
			{ChunkIndex: 0, InputURI: "s3://chunk0", InputSHA256: "sha"},
			{ChunkIndex: 1, InputURI: "s3://chunk1", InputSHA256: "sha"},
		},
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("sharded query_id job err = %v, want ErrInvalidInput", err)
	}
}

func TestClaimEmptyQueueReturnsNil(t *testing.T) {
	h := newHarness()
	c, err := h.claim.Execute(ctx, usecase.ClaimTaskInput{WorkerID: "w1", Workloads: []string{"w"}})
	if err != nil || c != nil {
		t.Errorf("claim on empty queue = (%v, %v), want (nil, nil)", c, err)
	}
}

func TestClaimRequiresWorkerID(t *testing.T) {
	h := newHarness()
	if _, err := h.claim.Execute(ctx, usecase.ClaimTaskInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestClaimSweepsExpiredLeaseFirst(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 1)
	// w1 leases it, then goes silent past the lease.
	taskID, _ := h.leaseOne(t, "w1", "w")
	h.clk.Advance(lease + time.Minute)

	// w2 claims: the sweep requeues the dead lease, so w2 gets the same task at attempt 2.
	c, err := h.claim.Execute(ctx, usecase.ClaimTaskInput{WorkerID: "w2", Workloads: []string{"w"}})
	if err != nil || c == nil {
		t.Fatalf("claim = (%v, %v)", c, err)
	}
	if c.TaskID != taskID || c.Attempt != 2 || c.LeaseOwner != "w2" {
		t.Errorf("got task=%v attempt=%d owner=%q", c.TaskID, c.Attempt, c.LeaseOwner)
	}
}

// --- RenewLease ----------------------------------------------------------

func TestRenewExtendsForHolder(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 1)
	taskID, attempt := h.leaseOne(t, "w1", "w")

	c, err := h.renew.Execute(ctx, usecase.RenewLeaseInput{TaskID: taskID, WorkerID: "w1", Attempt: attempt})
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !c.LeaseExpiresAt.Equal(h.clk.Now().Add(lease)) {
		t.Error("lease not extended to now+lease")
	}
}

func TestHeartbeatThenCompleteViaRunning(t *testing.T) {
	h := newHarness()
	jobID := h.seedJob(t, "w", 1)
	taskID, attempt := h.leaseOne(t, "w1", "w")

	// Heartbeat moves the task to running; completion must still work from there.
	if _, err := h.renew.Execute(ctx, usecase.RenewLeaseInput{TaskID: taskID, WorkerID: "w1", Attempt: attempt}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	artID := h.uploadResult(t, taskID, "w1", attempt)
	if _, err := h.complete.Execute(ctx, usecase.CompleteTaskInput{
		TaskID: taskID, WorkerID: "w1", Attempt: attempt, ResultArtifactID: artID,
	}); err != nil {
		t.Fatalf("complete after heartbeat: %v", err)
	}
	if prog, _ := h.status.Execute(ctx, jobID); prog.DeriveStatus() != domain.JobCompleted {
		t.Errorf("job status = %q, want completed", prog.DeriveStatus())
	}
}

func TestRenewRejectsForeignWorker(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 1)
	taskID, attempt := h.leaseOne(t, "w1", "w")

	_, err := h.renew.Execute(ctx, usecase.RenewLeaseInput{TaskID: taskID, WorkerID: "intruder", Attempt: attempt})
	if !errors.Is(err, domain.ErrLeaseConflict) {
		t.Errorf("err = %v, want ErrLeaseConflict", err)
	}
}

// --- CompleteTask --------------------------------------------------------

func TestCompleteHappyPathClosesJob(t *testing.T) {
	h := newHarness()
	jobID := h.seedJob(t, "w", 1)
	taskID, attempt := h.leaseOne(t, "w1", "w")
	artID := h.uploadResult(t, taskID, "w1", attempt)

	if _, err := h.complete.Execute(ctx, usecase.CompleteTaskInput{
		TaskID: taskID, WorkerID: "w1", Attempt: attempt, ResultArtifactID: artID,
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	prog, _ := h.status.Execute(ctx, jobID)
	if prog.DeriveStatus() != domain.JobCompleted {
		t.Errorf("job status = %q, want completed", prog.DeriveStatus())
	}
}

func TestCompleteRejectsForeignArtifact(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 2)
	// Lease two tasks; upload an artifact for taskA, try to complete taskB with it.
	taskA, attA := h.leaseOne(t, "w1", "w")
	taskB, attB := h.leaseOne(t, "w1", "w")
	artA := h.uploadResult(t, taskA, "w1", attA)

	_, err := h.complete.Execute(ctx, usecase.CompleteTaskInput{
		TaskID: taskB, WorkerID: "w1", Attempt: attB, ResultArtifactID: artA,
	})
	if !errors.Is(err, domain.ErrResultConflict) {
		t.Errorf("cross-task artifact: err = %v, want ErrResultConflict", err)
	}
}

func TestCompleteRejectsArtifactFromExpiredAttempt(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 1)
	taskID, attemptOne := h.leaseOne(t, "w1", "w")
	staleArtifact := h.uploadResult(t, taskID, "w1", attemptOne)

	h.clk.Advance(lease + time.Second)
	if _, err := h.expire.Execute(ctx); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	_, attemptTwo := h.leaseOne(t, "w2", "w")
	if attemptTwo != attemptOne+1 {
		t.Fatalf("attempt = %d, want %d", attemptTwo, attemptOne+1)
	}

	_, err := h.complete.Execute(ctx, usecase.CompleteTaskInput{
		TaskID: taskID, WorkerID: "w2", Attempt: attemptTwo, ResultArtifactID: staleArtifact,
	})
	if !errors.Is(err, domain.ErrResultConflict) {
		t.Errorf("stale-attempt artifact: err = %v, want ErrResultConflict", err)
	}
}

func TestUploadRejectsLeaseThatExpiresDuringStreaming(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 1)
	taskID, attempt := h.leaseOne(t, "w1", "w")
	h.uploadArt = usecase.NewUploadArtifact(
		h.tasks, h.arts, expiringBlobStore{BlobStore: h.blobs, clock: h.clk}, memstore.Tx{}, h.clk,
	)

	_, err := h.uploadArt.Execute(ctx, usecase.UploadArtifactInput{
		TaskID: taskID, WorkerID: "w1", Attempt: attempt,
		Filename: "result.csv", ContentType: "text/csv", Body: strings.NewReader("result"),
	})
	if !errors.Is(err, domain.ErrLeaseConflict) {
		t.Errorf("upload after lease expiry: err = %v, want ErrLeaseConflict", err)
	}
}

func TestCompleteIsIdempotentOnReplay(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 1)
	taskID, attempt := h.leaseOne(t, "w1", "w")
	artID := h.uploadResult(t, taskID, "w1", attempt)
	in := usecase.CompleteTaskInput{TaskID: taskID, WorkerID: "w1", Attempt: attempt, ResultArtifactID: artID}

	if _, err := h.complete.Execute(ctx, in); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	if _, err := h.complete.Execute(ctx, in); err != nil {
		t.Errorf("replay must be idempotent, got %v", err)
	}
}

// --- FailTask ------------------------------------------------------------

func TestFailRequeuesWhileAttemptsRemain(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 1)
	taskID, attempt := h.leaseOne(t, "w1", "w")

	task, err := h.fail.Execute(ctx, usecase.FailTaskInput{
		TaskID: taskID, WorkerID: "w1", Attempt: attempt,
		ErrorCode: "boom", ErrorMessage: "exploded", Retryable: true,
	})
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if task.Status != domain.TaskPending {
		t.Errorf("status = %q, want pending (requeued)", task.Status)
	}
	// It should be claimable again.
	if c, _ := h.claim.Execute(ctx, usecase.ClaimTaskInput{WorkerID: "w2", Workloads: []string{"w"}}); c == nil {
		t.Error("requeued task should be claimable")
	}
}

// --- CreateJob / status --------------------------------------------------

func TestCreateJobFansOutIntoTasks(t *testing.T) {
	h := newHarness()
	jobID := h.seedJob(t, "w", 3)
	prog, err := h.status.Execute(ctx, jobID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if prog.Total != 3 || prog.Pending != 3 {
		t.Errorf("progress total=%d pending=%d, want 3/3", prog.Total, prog.Pending)
	}
}

// --- RegisterWorker ------------------------------------------------------

func TestRegisterWorkerPersists(t *testing.T) {
	h := newHarness()
	w, err := h.register.Execute(ctx, usecase.RegisterWorkerInput{Name: "lab", Capabilities: []string{"w"}})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := h.work.Get(ctx, w.ID)
	if err != nil || got.Status != domain.WorkerOnline {
		t.Errorf("worker not stored online: %v %v", got, err)
	}
}

func TestHeartbeatTracksWorkerLivenessAndReaperMarksOffline(t *testing.T) {
	h := newHarness()
	w, err := h.register.Execute(ctx, usecase.RegisterWorkerInput{Name: "lab", Capabilities: []string{"w"}})
	if err != nil {
		t.Fatal(err)
	}
	wid := w.ID.String() // a registered worker heartbeats with its UUID
	h.seedJob(t, "w", 1)

	c, err := h.claim.Execute(ctx, usecase.ClaimTaskInput{WorkerID: wid, Workloads: []string{"w"}})
	if err != nil || c == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := h.renew.Execute(ctx, usecase.RenewLeaseInput{TaskID: c.TaskID, WorkerID: wid, Attempt: c.Attempt}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if got, _ := h.work.Get(ctx, w.ID); got.Status != domain.WorkerOnline {
		t.Errorf("worker status = %q, want online after heartbeat", got.Status)
	}

	// Go silent past the threshold; the reaper marks it offline.
	offline := usecase.NewMarkWorkersOffline(h.work, h.clk, 30*time.Second)
	h.clk.Advance(time.Minute)
	n, err := offline.Execute(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reaper marked %d offline (err %v), want 1", n, err)
	}
	if got, _ := h.work.Get(ctx, w.ID); got.Status != domain.WorkerOffline {
		t.Errorf("worker status = %q, want offline after reaper", got.Status)
	}
}

func TestRegisterWorkerRejectsNoCapabilities(t *testing.T) {
	h := newHarness()
	if _, err := h.register.Execute(ctx, usecase.RegisterWorkerInput{Name: "lab"}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

// --- UploadArtifact ------------------------------------------------------

func TestUploadArtifactRejectsForeignWorker(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 1)
	taskID, attempt := h.leaseOne(t, "w1", "w")

	_, err := h.uploadArt.Execute(ctx, usecase.UploadArtifactInput{
		TaskID: taskID, WorkerID: "intruder", Attempt: attempt,
		Filename: "r.csv", ContentType: "text/csv", Body: strings.NewReader("x"),
	})
	if !errors.Is(err, domain.ErrLeaseConflict) {
		t.Errorf("err = %v, want ErrLeaseConflict", err)
	}
}

func TestUploadArtifactIsIdempotentPerTaskAttempt(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 1)
	taskID, attempt := h.leaseOne(t, "w1", "w")
	first := h.uploadResult(t, taskID, "w1", attempt)
	second, err := h.uploadArt.Execute(ctx, usecase.UploadArtifactInput{
		TaskID: taskID, WorkerID: "w1", Attempt: attempt,
		Filename: "retry.csv", ContentType: "text/csv", Body: strings.NewReader("different bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first {
		t.Errorf("retry artifact = %s, want existing %s", second.ID, first)
	}
}

func TestDownloadArtifactRoundTrips(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 1)
	taskID, attempt := h.leaseOne(t, "w1", "w")
	artID := h.uploadResult(t, taskID, "w1", attempt)

	art, rc, err := h.downloadArt.Execute(ctx, artID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer rc.Close()
	if art.Kind != domain.ArtifactPartialResult {
		t.Errorf("kind = %q", art.Kind)
	}
}

// --- SubmitDataset / GetTaskInput ---------------------------------------

func TestSubmitDatasetChunksAndServesInput(t *testing.T) {
	h := newHarness()
	tsv := "chembl_id\tcanonical_smiles\nA\tCC\nB\tCCC\nC\tCCCC\nD\tCCCCC\nE\tCCCCCC\n"

	res, err := h.submit.Execute(ctx, usecase.SubmitDatasetInput{
		Workload: "similarity-search", Parameters: map[string]any{"query_smiles": "CCO"}, RowsPerShard: 2, Filename: "chembl.tsv",
		ContentType: "text/tab-separated-values", Body: strings.NewReader(tsv),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.TaskCount != 3 { // 5 rows / 2
		t.Fatalf("task_count = %d, want 3", res.TaskCount)
	}

	// The job now has three claimable shard tasks; each serves its own input.
	prog, _ := h.status.Execute(ctx, res.JobID)
	if prog.Total != 3 {
		t.Errorf("job total = %d, want 3", prog.Total)
	}

	c, err := h.claim.Execute(ctx, usecase.ClaimTaskInput{WorkerID: "w1", Workloads: []string{"similarity-search"}})
	if err != nil || c == nil {
		t.Fatalf("claim shard: %v", err)
	}
	if c.InputArtifactID == nil {
		t.Fatal("shard task must reference an input artifact")
	}
	art, rc, err := h.getInput.Execute(ctx, c.TaskID)
	if err != nil {
		t.Fatalf("get input: %v", err)
	}
	defer rc.Close()
	if art.Kind != domain.ArtifactShard {
		t.Errorf("input kind = %q, want shard", art.Kind)
	}
}

func TestSubmitDatasetLimitsRowsBeforeCreatingShards(t *testing.T) {
	h := newHarness()
	tsv := "chembl_id\tcanonical_smiles\nA\tCC\nB\tCCC\nC\tCCCC\nD\tCCCCC\nE\tCCCCCC\n"
	res, err := h.submit.Execute(ctx, usecase.SubmitDatasetInput{
		Workload: "similarity-search", Parameters: map[string]any{"query_smiles": "CCO"}, RowsPerShard: 2, MaxRows: 3, Filename: "chembl.tsv",
		ContentType: "text/tab-separated-values", Body: strings.NewReader(tsv),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TaskCount != 2 {
		t.Fatalf("task_count = %d, want 2", res.TaskCount)
	}
}

func TestSubmitDatasetRejectsUnsupportedDistributedWorkloads(t *testing.T) {
	h := newHarness()
	_, err := h.submit.Execute(ctx, usecase.SubmitDatasetInput{
		Workload: "similarity-graph", Parameters: map[string]any{"threshold": 0.7}, RowsPerShard: 2,
		Filename: "chembl.tsv", ContentType: "text/tab-separated-values",
		Body: strings.NewReader("chembl_id\tcanonical_smiles\nA\tCC\n"),
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("graph submission err = %v, want ErrInvalidInput", err)
	}
	_, err = h.submit.Execute(ctx, usecase.SubmitDatasetInput{
		Workload: "similarity-search", Parameters: map[string]any{"query_id": "CHEMBL1"}, RowsPerShard: 2,
		Filename: "chembl.tsv", ContentType: "text/tab-separated-values",
		Body: strings.NewReader("chembl_id\tcanonical_smiles\nA\tCC\n"),
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("query_id submission err = %v, want ErrInvalidInput", err)
	}
}

func TestCancelJobInvalidatesClaimedAndPendingTasks(t *testing.T) {
	h := newHarness()
	jobID := h.seedJob(t, "w", 3)
	claimed, err := h.claim.Execute(ctx, usecase.ClaimTaskInput{WorkerID: "w1", Workloads: []string{"w"}})
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v", err)
	}
	cancelled, err := h.cancel.Execute(ctx, jobID)
	if err != nil || cancelled != 3 {
		t.Fatalf("cancel = (%d, %v), want (3, nil)", cancelled, err)
	}
	if _, err := h.renew.Execute(ctx, usecase.RenewLeaseInput{TaskID: claimed.TaskID, WorkerID: "w1", Attempt: claimed.Attempt}); !errors.Is(err, domain.ErrTaskNotLeased) {
		t.Errorf("cancelled lease heartbeat = %v, want ErrTaskNotLeased", err)
	}
	progress, err := h.status.Execute(ctx, jobID)
	if err != nil || progress.DeriveStatus() != domain.JobCancelled || progress.Cancelled != 3 {
		t.Errorf("cancelled progress = %+v, err = %v", progress, err)
	}
	if cancelled, err := h.cancel.Execute(ctx, jobID); err != nil || cancelled != 0 {
		t.Errorf("second cancel = (%d, %v), want (0, nil)", cancelled, err)
	}
}

func TestGetTaskInputMissingForURITask(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 1) // URI-based task, no coordinator-stored input
	taskID, _ := h.leaseOne(t, "w1", "w")

	if _, _, err := h.getInput.Execute(ctx, taskID); !errors.Is(err, domain.ErrArtifactNotFound) {
		t.Errorf("err = %v, want ErrArtifactNotFound", err)
	}
}

// --- ExpireLeases --------------------------------------------------------

func TestExpireLeasesReclaims(t *testing.T) {
	h := newHarness()
	h.seedJob(t, "w", 1)
	h.leaseOne(t, "w1", "w")
	h.clk.Advance(lease + time.Minute)

	n, err := h.expire.Execute(ctx)
	if err != nil || n != 1 {
		t.Errorf("expire = (%d, %v), want (1, nil)", n, err)
	}
}

func TestFinalLeaseExpiryPersistsFailedJobAndCannotBeCancelled(t *testing.T) {
	h := newHarness()
	jobID := h.seedJob(t, "w", 1)
	for attempt := 1; attempt <= domain.DefaultMaxAttempts; attempt++ {
		h.leaseOne(t, "w1", "w")
		h.clk.Advance(lease + time.Second)
		if _, err := h.expire.Execute(ctx); err != nil {
			t.Fatalf("expire attempt %d: %v", attempt, err)
		}
	}
	progress, err := h.status.Execute(ctx, jobID)
	if err != nil || progress.Job.Status != domain.JobFailed || progress.DeriveStatus() != domain.JobFailed {
		t.Fatalf("progress = %+v, err = %v; want persisted failed job", progress, err)
	}
	if _, err := h.cancel.Execute(ctx, jobID); !errors.Is(err, domain.ErrJobNotCancellable) {
		t.Errorf("cancel terminal lease failure = %v, want ErrJobNotCancellable", err)
	}
}
