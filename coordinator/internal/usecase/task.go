package usecase

import (
	"context"
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
	clock         Clock
	leaseDuration time.Duration
}

func NewClaimTask(tasks TaskRepository, clock Clock, leaseDuration time.Duration) *ClaimTask {
	return &ClaimTask{tasks: tasks, clock: clock, leaseDuration: leaseDuration}
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
	now := uc.clock.Now()

	if _, err := uc.tasks.ExpireLeases(ctx, now); err != nil {
		return nil, err
	}

	task, err := uc.tasks.ClaimNext(ctx, ClaimFilter{
		Workloads:  in.Workloads,
		Owner:      in.WorkerID,
		Now:        now,
		LeaseUntil: now.Add(uc.leaseDuration),
	})
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, nil // empty queue is a normal state, not an error
	}

	claimed := task.AsClaimed()
	return &claimed, nil
}

// --- RenewLease ----------------------------------------------------------

type RenewLease struct {
	tasks         TaskRepository
	tx            TxManager
	clock         Clock
	leaseDuration time.Duration
}

func NewRenewLease(tasks TaskRepository, tx TxManager, clock Clock, leaseDuration time.Duration) *RenewLease {
	return &RenewLease{tasks: tasks, tx: tx, clock: clock, leaseDuration: leaseDuration}
}

// Execute is a read-modify-write, so it runs inside a transaction with the row
// locked: two concurrent heartbeats must not interleave into a lost update.
// Whether the caller may renew at all is decided by the entity, not here.
func (uc *RenewLease) Execute(ctx context.Context, in RenewLeaseInput) (*domain.ClaimedTask, error) {
	var claimed domain.ClaimedTask

	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		task, err := uc.tasks.GetForUpdate(ctx, in.TaskID)
		if err != nil {
			return err
		}
		if err := task.RenewLease(in.WorkerID, in.Attempt, uc.clock.Now().Add(uc.leaseDuration)); err != nil {
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
	return &claimed, nil
}

// --- CompleteTask --------------------------------------------------------

type CompleteTask struct {
	tasks     TaskRepository
	jobs      JobRepository
	artifacts ArtifactRepository
	tx        TxManager
	clock     Clock
}

func NewCompleteTask(tasks TaskRepository, jobs JobRepository, artifacts ArtifactRepository,
	tx TxManager, clock Clock) *CompleteTask {
	return &CompleteTask{tasks: tasks, jobs: jobs, artifacts: artifacts, tx: tx, clock: clock}
}

// Execute applies the result and, when that was the job's last outstanding
// task, closes the job in the same transaction — so a caller who sees a
// completed task never observes its job still marked running.
//
// Lease ownership, staleness, and idempotent replays are all decided by
// Task.CompleteWith; this use case only orchestrates.
func (uc *CompleteTask) Execute(ctx context.Context, in CompleteTaskInput) (*domain.Task, error) {
	var out *domain.Task

	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		task, err := uc.tasks.GetForUpdate(ctx, in.TaskID)
		if err != nil {
			return err
		}
		// Rule 10: never trust a worker-supplied artifact reference. The result
		// must be an artifact the coordinator itself stored for *this* task.
		if err := uc.verifyResultArtifact(ctx, in.TaskID, in.ResultArtifactID); err != nil {
			return err
		}
		now := uc.clock.Now()
		before := task.Version
		if err := task.CompleteWith(in.ResultArtifactID, in.Metrics,
			in.WorkerID, in.Attempt, now); err != nil {
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

// verifyResultArtifact enforces that the referenced artifact was stored by the
// coordinator for this exact task. It stops a worker from completing task B with
// an artifact it uploaded for task A, and from naming an id that isn't a result.
func (uc *CompleteTask) verifyResultArtifact(ctx context.Context, taskID, artifactID uuid.UUID) error {
	art, err := uc.artifacts.Get(ctx, artifactID)
	if err != nil {
		return err
	}
	if art.TaskID == nil || *art.TaskID != taskID || art.Kind != domain.ArtifactPartialResult {
		return domain.ErrResultConflict
	}
	return nil
}

// --- FailTask ------------------------------------------------------------

type FailTask struct {
	tasks TaskRepository
	jobs  JobRepository
	tx    TxManager
	clock Clock
}

func NewFailTask(tasks TaskRepository, jobs JobRepository, tx TxManager, clock Clock) *FailTask {
	return &FailTask{tasks: tasks, jobs: jobs, tx: tx, clock: clock}
}

// Execute delegates the requeue-or-terminate decision to Task.Fail, then keeps
// the parent job's status consistent in the same transaction.
func (uc *FailTask) Execute(ctx context.Context, in FailTaskInput) (*domain.Task, error) {
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
	clock Clock
}

func NewExpireLeases(tasks TaskRepository, clock Clock) *ExpireLeases {
	return &ExpireLeases{tasks: tasks, clock: clock}
}

// Execute reports how many tasks were reclaimed.
//
// The sweep is one set-based statement rather than a load-decide-save loop:
// several coordinators run it concurrently, and a single atomic UPDATE makes
// the duplicate work harmless — the loser simply updates 0 rows.
func (uc *ExpireLeases) Execute(ctx context.Context) (int64, error) {
	return uc.tasks.ExpireLeases(ctx, uc.clock.Now())
}
