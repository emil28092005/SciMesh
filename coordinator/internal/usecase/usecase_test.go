package usecase_test

import (
	"context"
	"errors"
	"fmt"
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
	h.submit = usecase.NewSubmitDataset(h.blobs, h.arts, h.jobs, h.tasks, tx, h.clk)
	h.claim = usecase.NewClaimTask(h.tasks, h.clk, lease)
	h.renew = usecase.NewRenewLease(h.tasks, tx, h.clk, lease)
	h.complete = usecase.NewCompleteTask(h.tasks, h.jobs, h.arts, tx, h.clk)
	h.fail = usecase.NewFailTask(h.tasks, h.jobs, tx, h.clk)
	h.status = usecase.NewGetJobStatus(h.jobs, h.tasks)
	h.results = usecase.NewListResults(h.tasks)
	h.register = usecase.NewRegisterWorker(h.work, h.clk)
	h.uploadArt = usecase.NewUploadArtifact(h.tasks, h.arts, h.blobs, h.clk)
	h.downloadArt = usecase.NewDownloadArtifact(h.arts, h.blobs)
	h.getInput = usecase.NewGetTaskInput(h.tasks, h.arts, h.blobs)
	h.expire = usecase.NewExpireLeases(h.tasks, h.clk)
	return h
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
	tsv := "id\tsmiles\nA\tCC\nB\tCCC\nC\tCCCC\nD\tCCCCC\nE\tCCCCCC\n"

	res, err := h.submit.Execute(ctx, usecase.SubmitDatasetInput{
		Workload: "w", RowsPerShard: 2, Filename: "chembl.tsv",
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

	c, err := h.claim.Execute(ctx, usecase.ClaimTaskInput{WorkerID: "w1", Workloads: []string{"w"}})
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
