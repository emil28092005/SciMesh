package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// Task operations: the worker-facing lifecycle of a single chunk.
//
//	ClaimTask    lease the next available task
//	RenewLease   extend a held lease (heartbeat)
//	CompleteTask record a successful result
//	FailTask     record a failure
//	ExpireLeases reclaim leases that elapsed without a heartbeat

// --- ClaimTask -----------------------------------------------------------

type ClaimTask struct {
	tasks         TaskRepository
	jobs          JobRepository
	workers       WorkerRepository
	tx            TxManager
	clock         Clock
	leaseDuration time.Duration
}

func NewClaimTask(tasks TaskRepository, jobs JobRepository, workers WorkerRepository, tx TxManager, clock Clock, leaseDuration time.Duration) *ClaimTask {
	return &ClaimTask{tasks: tasks, jobs: jobs, workers: workers, tx: tx, clock: clock, leaseDuration: leaseDuration}
}

// Execute reclaims elapsed leases first, then hands out one task.
//
// Sweeping before claiming matters: otherwise a task abandoned by a dead worker
// stays invisible until the reaper's next tick, and a waiting worker is told the
// queue is empty while work sits idle.
//
// This use case is thin by design — the atomicity that makes claiming correct
// lives in one SQL statement behind ClaimNext, and splitting it across the layer
// boundary would break it.
func (uc *ClaimTask) Execute(ctx context.Context, in ClaimTaskInput) (*domain.ClaimedTask, error) {
	if in.WorkerID == "" {
		return nil, domain.ErrInvalidInput
	}
	workloads := in.Workloads
	var voterOwner *uuid.UUID
	if workerID, err := uuid.Parse(in.WorkerID); err == nil {
		worker, err := uc.workers.Get(ctx, workerID)
		if err != nil {
			return nil, err
		}
		// Bind the caller to the worker it claims as. A JWT-authenticated
		// volunteer may operate only its own workers; without this the trust
		// tier would be read off a caller-supplied worker_id, letting anyone who
		// knows a trusted worker's id claim as it. A shared-token caller (no
		// requester) is a lab operator and may act as any worker.
		if err := authorizeWorkerOwner(ctx, uc.workers, in.WorkerID); err != nil {
			return nil, err
		}
		// An untrusted volunteer may claim, but never a chunk its owner has
		// already voted on — so quorum needs genuinely independent computations.
		if worker.TrustLevel == domain.WorkerUntrusted {
			voterOwner = worker.OwnerID
		}
		// Never trust caller-supplied capabilities: registration is the durable
		// worker identity and its allowlist.
		workloads = worker.Capabilities
	}
	var claimed *domain.ClaimedTask
	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		now := uc.clock.Now()
		affectedJobs, err := uc.tasks.ExpireLeases(ctx, now)
		if err != nil {
			return err
		}
		if err := syncExpiredJobStatuses(ctx, uc.jobs, uc.tasks, affectedJobs, now); err != nil {
			return err
		}

		task, err := uc.tasks.ClaimNext(ctx, ClaimFilter{
			Workloads:  workloads,
			Owner:      in.WorkerID,
			Now:        now,
			LeaseUntil: now.Add(uc.leaseDuration),
			VoterOwner: voterOwner,
		})
		if err != nil {
			return err
		}
		if task != nil {
			value := task.AsClaimed()
			claimed = &value
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil // nil means an empty queue
}

// --- RenewLease ----------------------------------------------------------

type RenewLease struct {
	tasks         TaskRepository
	workers       WorkerRepository
	tx            TxManager
	clock         Clock
	leaseDuration time.Duration
}

func NewRenewLease(tasks TaskRepository, workers WorkerRepository, tx TxManager,
	clock Clock, leaseDuration time.Duration) *RenewLease {
	return &RenewLease{tasks: tasks, workers: workers, tx: tx, clock: clock, leaseDuration: leaseDuration}
}

// Execute is a read-modify-write, so it runs inside a transaction with the row
// locked: two concurrent heartbeats must not interleave into a lost update.
// Whether the caller may renew at all is decided by the entity, not here.
func (uc *RenewLease) Execute(ctx context.Context, in RenewLeaseInput) (*domain.ClaimedTask, error) {
	if err := authorizeWorkerOwner(ctx, uc.workers, in.WorkerID); err != nil {
		return nil, err
	}
	var claimed domain.ClaimedTask

	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		task, err := uc.tasks.GetForUpdate(ctx, in.TaskID)
		if err != nil {
			return err
		}
		now := uc.clock.Now()
		if err := task.RenewLease(in.WorkerID, in.Attempt, now, now.Add(uc.leaseDuration)); err != nil {
			return err
		}
		if err := uc.tasks.Update(ctx, task); err != nil {
			return err
		}
		claimed = task.AsClaimed()
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Best-effort worker liveness, outside the task transaction so it can never
	// fail the heartbeat. Only registered workers (a UUID worker_id) are tracked.
	if id, perr := uuid.Parse(in.WorkerID); perr == nil {
		_ = uc.workers.Touch(ctx, id, uc.clock.Now())
	}
	return &claimed, nil
}

// --- CompleteTask --------------------------------------------------------

type CompleteTask struct {
	tasks     TaskRepository
	jobs      JobRepository
	artifacts ArtifactRepository
	workers   WorkerRepository
	results   TaskResultRepository
	tx        TxManager
	clock     Clock
	// quorum is how many distinct owners must agree on an untrusted result
	// before it is accepted; a trusted worker's result is accepted directly.
	quorum int
}

func NewCompleteTask(tasks TaskRepository, jobs JobRepository, artifacts ArtifactRepository,
	workers WorkerRepository, results TaskResultRepository, tx TxManager, clock Clock, quorum int) *CompleteTask {
	if quorum < 1 {
		quorum = 2
	}
	return &CompleteTask{tasks: tasks, jobs: jobs, artifacts: artifacts, workers: workers,
		results: results, tx: tx, clock: clock, quorum: quorum}
}

// Execute applies the result and, when that was the job's last outstanding
// task, closes the job in the same transaction — so a caller who sees a
// completed task never observes its job still marked running.
//
// Lease ownership, staleness, and idempotent replays are all decided by
// Task.CompleteWith; this use case only orchestrates.
func (uc *CompleteTask) Execute(ctx context.Context, in CompleteTaskInput) (*domain.Task, error) {
	if err := authorizeWorkerOwner(ctx, uc.workers, in.WorkerID); err != nil {
		return nil, err
	}
	var out *domain.Task

	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		task, err := uc.tasks.GetForUpdate(ctx, in.TaskID)
		if err != nil {
			return err
		}
		// Rule 10: never trust a worker-supplied artifact reference. The result
		// must be an artifact the coordinator itself stored for *this* task.
		art, err := uc.verifyResultArtifact(ctx, in.TaskID, in.Attempt, in.ResultArtifactID)
		if err != nil {
			return err
		}

		trusted, ownerID, err := uc.workerTrust(ctx, in.WorkerID)
		if err != nil {
			return err
		}
		now := uc.clock.Now()

		// Untrusted (volunteer) worker: record a vote and only complete once a
		// quorum of distinct owners agree; otherwise return the task to the queue.
		if !trusted {
			return uc.recordVote(ctx, task, in, art, ownerID, now, &out)
		}

		// Trusted worker (lab token, verified, or admin): accept directly.
		before := task.Version
		if err := task.CompleteWith(in.ResultArtifactID, in.Metrics, in.WorkerID, in.Attempt, now); err != nil {
			return err
		}
		out = task
		// A replay of an already-recorded result leaves the entity untouched.
		// Writing anyway would fail the optimistic-concurrency guard (the stored
		// version already equals ours) and turn an idempotent call into a 409.
		if task.Version == before {
			return nil
		}
		if err := uc.tasks.Update(ctx, task); err != nil {
			return err
		}
		return syncJobStatus(ctx, uc.jobs, uc.tasks, task.JobID, now)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// recordVote handles an untrusted result: it stores the vote, then completes the
// task when the submitter's result hash has reached quorum, or returns the task
// to the queue so another owner can compute it independently.
func (uc *CompleteTask) recordVote(ctx context.Context, task *domain.Task, in CompleteTaskInput,
	art *domain.Artifact, ownerID uuid.UUID, now time.Time, out **domain.Task) error {

	*out = task
	if task.Status == domain.TaskCompleted {
		return nil // already settled by an earlier quorum; nothing to record
	}
	if err := uc.results.RecordVote(ctx, task.ID, ownerID, art.SHA256, in.ResultArtifactID); err != nil {
		return err
	}
	agree, err := uc.results.CountAgreeing(ctx, task.ID, art.SHA256)
	if err != nil {
		return err
	}

	if agree >= uc.quorum {
		// The submitter's own (already verified) artifact carries the winning
		// hash, so complete with it.
		if err := task.CompleteWith(in.ResultArtifactID, in.Metrics, in.WorkerID, in.Attempt, now); err != nil {
			return err
		}
	} else if err := task.ReleaseAfterVote(in.WorkerID, in.Attempt, now); err != nil {
		return err
	}

	if err := uc.tasks.Update(ctx, task); err != nil {
		return err
	}
	return syncJobStatus(ctx, uc.jobs, uc.tasks, task.JobID, now)
}

// workerTrust reports whether the worker's results are accepted directly, and
// the owner to attribute a vote to when they are not.
func (uc *CompleteTask) workerTrust(ctx context.Context, workerID string) (trusted bool, ownerID uuid.UUID, err error) {
	// When the worker can't be resolved, default to trusted — the pre-quorum
	// behaviour. This is safe because completing a task requires holding its
	// lease, and the lease owner is always a real registered worker whose trust
	// is therefore known; only an untrusted worker ever takes the quorum path.
	id, err := uuid.Parse(workerID)
	if err != nil {
		// An unparseable worker id means the worker can't be resolved; fall back
		// to the trusted default rather than surfacing the parse error.
		return true, uuid.Nil, nil //nolint:nilerr // unresolvable worker → trusted (pre-quorum default)
	}
	w, err := uc.workers.Get(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrWorkerNotFound) {
			return true, uuid.Nil, nil
		}
		return false, uuid.Nil, err
	}
	if w.TrustLevel != domain.WorkerUntrusted {
		return true, uuid.Nil, nil
	}
	if w.OwnerID == nil {
		// An untrusted worker always has an owner (it registered via a user JWT);
		// a missing one is a data error, not a silent trust upgrade.
		return false, uuid.Nil, domain.ErrInvalidInput
	}
	return false, *w.OwnerID, nil
}

// verifyResultArtifact enforces that the referenced artifact was stored by the
// coordinator for this exact task. It stops a worker from completing task B with
// an artifact it uploaded for task A, and from naming an id that isn't a result.
func (uc *CompleteTask) verifyResultArtifact(ctx context.Context, taskID uuid.UUID, attempt int, artifactID uuid.UUID) (*domain.Artifact, error) {
	art, err := uc.artifacts.Get(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	if art.TaskID == nil || *art.TaskID != taskID || art.Attempt == nil || *art.Attempt != attempt || art.Kind != domain.ArtifactPartialResult {
		return nil, domain.ErrResultConflict
	}
	return art, nil
}

// --- FailTask ------------------------------------------------------------

type FailTask struct {
	tasks   TaskRepository
	jobs    JobRepository
	workers WorkerRepository
	tx      TxManager
	clock   Clock
}

func NewFailTask(tasks TaskRepository, jobs JobRepository, workers WorkerRepository, tx TxManager, clock Clock) *FailTask {
	return &FailTask{tasks: tasks, jobs: jobs, workers: workers, tx: tx, clock: clock}
}

// Execute delegates the requeue-or-terminate decision to Task.Fail, then keeps
// the parent job's status consistent in the same transaction.
func (uc *FailTask) Execute(ctx context.Context, in FailTaskInput) (*domain.Task, error) {
	if err := authorizeWorkerOwner(ctx, uc.workers, in.WorkerID); err != nil {
		return nil, err
	}
	var out *domain.Task

	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		task, err := uc.tasks.GetForUpdate(ctx, in.TaskID)
		if err != nil {
			return err
		}
		now := uc.clock.Now()
		if err := task.Fail(in.WorkerID, in.Attempt, in.ErrorCode, in.ErrorMessage, in.Retryable, now); err != nil {
			return err
		}
		if err := uc.tasks.Update(ctx, task); err != nil {
			return err
		}
		out = task
		return syncJobStatus(ctx, uc.jobs, uc.tasks, task.JobID, now)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// --- ExpireLeases --------------------------------------------------------

type ExpireLeases struct {
	tasks TaskRepository
	jobs  JobRepository
	tx    TxManager
	clock Clock
}

func NewExpireLeases(tasks TaskRepository, jobs JobRepository, tx TxManager, clock Clock) *ExpireLeases {
	return &ExpireLeases{tasks: tasks, jobs: jobs, tx: tx, clock: clock}
}

// Execute reclaims elapsed tasks and persists the state of every affected job.
//
// The sweep is one set-based statement rather than a load-decide-save loop:
// several coordinators run it concurrently, and a single atomic UPDATE makes
// the duplicate work harmless — the loser simply updates 0 rows.
func (uc *ExpireLeases) Execute(ctx context.Context) (int64, error) {
	var affected []uuid.UUID
	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		now := uc.clock.Now()
		var err error
		affected, err = uc.tasks.ExpireLeases(ctx, now)
		if err != nil {
			return err
		}
		return syncExpiredJobStatuses(ctx, uc.jobs, uc.tasks, affected, now)
	})
	return int64(len(affected)), err
}
