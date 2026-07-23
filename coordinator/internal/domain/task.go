// Package domain holds SciMesh's entities and the rules that govern them. It
// is the innermost layer: it imports nothing from this module and knows nothing
// about HTTP, SQL, or configuration. Every state transition a task can undergo
// is a method here, so the rules are unit-testable without a database.
package domain

import (
	"time"

	"github.com/google/uuid"
)

type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskLeased    TaskStatus = "leased"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)

// ErrCodeLeaseExpired marks tasks failed by the reaper rather than by a worker.
const ErrCodeLeaseExpired = "lease_expired"

// Task is one independently executable chunk of a job.
//
// Nullable columns are pointers so "no lease" stays distinguishable from
// "lease owned by the empty string" — a plain string cannot express both.
type Task struct {
	ID               uuid.UUID
	JobID            uuid.UUID
	ChunkIndex       int
	Workload         string
	InputURI         string
	InputSHA256      string
	Parameters       map[string]any
	Status           TaskStatus
	Attempt          int
	MaxAttempts      int
	LeaseOwner       *string
	LeaseExpiresAt   *time.Time
	ResultArtifactID *uuid.UUID
	Metrics          map[string]any
	ErrorCode        *string
	ErrorMessage     *string
	CreatedAt        time.Time
	StartedAt        *time.Time
	CompletedAt      *time.Time
	Version          int
}

// NewTask builds a pending task. maxAttempts <= 0 falls back to the default.
func NewTask(jobID uuid.UUID, chunkIndex int, workload, inputURI, inputSHA256 string,
	params map[string]any, maxAttempts int, now time.Time) (*Task, error) {

	if inputURI == "" {
		return nil, ErrInvalidInput
	}
	if inputSHA256 == "" {
		return nil, ErrInvalidInput // checksum is mandatory: workers verify inputs
	}
	if chunkIndex < 0 {
		return nil, ErrInvalidInput
	}
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	return &Task{
		ID:          uuid.New(),
		JobID:       jobID,
		ChunkIndex:  chunkIndex,
		Workload:    workload,
		InputURI:    inputURI,
		InputSHA256: inputSHA256,
		Parameters:  params,
		Status:      TaskPending,
		Attempt:     0,
		MaxAttempts: maxAttempts,
		CreatedAt:   now,
	}, nil
}

// DefaultMaxAttempts applies when a task does not specify its own ceiling.
const DefaultMaxAttempts = 3

// CanRetry reports whether any attempts remain.
func (t *Task) CanRetry() bool { return t.Attempt < t.MaxAttempts }

// IsLeaseHeldBy reports whether worker currently holds this task at attempt.
func (t *Task) IsLeaseHeldBy(worker string, attempt int) bool {
	return t.LeaseOwner != nil && *t.LeaseOwner == worker && t.Attempt == attempt
}

// AsClaimed projects the task into the trimmed view handed to a worker:
// everything needed to execute, nothing it has no business seeing.
func (t *Task) AsClaimed() ClaimedTask {
	ct := ClaimedTask{
		TaskID:      t.ID,
		JobID:       t.JobID,
		ChunkIndex:  t.ChunkIndex,
		Workload:    t.Workload,
		InputURI:    t.InputURI,
		InputSHA256: t.InputSHA256,
		Parameters:  t.Parameters,
		Attempt:     t.Attempt,
	}
	if t.LeaseOwner != nil {
		ct.LeaseOwner = *t.LeaseOwner
	}
	if t.LeaseExpiresAt != nil {
		ct.LeaseExpiresAt = *t.LeaseExpiresAt
	}
	return ct
}

// verifyLease is the guard every worker-driven transition shares: the caller
// must own the lease and reference the attempt it was granted.
func (t *Task) verifyLease(worker string, attempt int) error {
	if t.Status != TaskLeased {
		return ErrTaskNotLeased
	}
	if t.LeaseOwner == nil || *t.LeaseOwner != worker {
		return ErrLeaseConflict
	}
	if t.Attempt != attempt {
		return ErrStaleAttempt
	}
	return nil
}

// RenewLease extends the lease of the worker that holds it.
func (t *Task) RenewLease(worker string, attempt int, until time.Time) error {
	if err := t.verifyLease(worker, attempt); err != nil {
		return err
	}
	t.LeaseExpiresAt = &until
	t.Version++
	return nil
}

// CompleteWith records a successful result.
//
// Idempotency comes first deliberately: a worker whose network dropped will
// retry the same manifest, and that must succeed rather than trip the lease
// check on a task the coordinator already finished. A *different* manifest for
// an already-completed task is a genuine conflict.
func (t *Task) CompleteWith(resultArtifactID uuid.UUID, metrics map[string]any,
	worker string, attempt int, now time.Time) error {

	if resultArtifactID == uuid.Nil {
		return ErrInvalidInput
	}

	if t.Status == TaskCompleted {
		if t.Attempt == attempt && t.ResultArtifactID != nil && *t.ResultArtifactID == resultArtifactID {
			return nil // same attempt, same artifact — replay of a successful call
		}
		return ErrResultConflict
	}

	if err := t.verifyLease(worker, attempt); err != nil {
		return err
	}

	t.Status = TaskCompleted
	t.ResultArtifactID = &resultArtifactID
	t.Metrics = metrics
	t.CompletedAt = &now
	t.LeaseOwner = nil
	t.LeaseExpiresAt = nil
	t.ErrorCode = nil
	t.ErrorMessage = nil
	t.Version++
	return nil
}

// Fail records a worker-reported failure. A retryable failure with attempts
// left returns the task to the queue; otherwise it terminates as failed.
func (t *Task) Fail(worker string, attempt int, code, message string, retryable bool, now time.Time) error {
	if err := t.verifyLease(worker, attempt); err != nil {
		return err
	}
	t.ErrorCode = &code
	t.ErrorMessage = &message
	t.LeaseOwner = nil
	t.LeaseExpiresAt = nil
	t.Version++

	if retryable && t.CanRetry() {
		t.Status = TaskPending
		return nil
	}
	t.Status = TaskFailed
	t.CompletedAt = &now
	return nil
}

// ExpireLease is applied by the reaper when a lease elapses without a
// heartbeat: requeue while attempts remain, otherwise fail terminally.
func (t *Task) ExpireLease(now time.Time) {
	if t.Status != TaskLeased {
		return
	}
	t.LeaseOwner = nil
	t.LeaseExpiresAt = nil
	t.Version++

	if t.CanRetry() {
		t.Status = TaskPending
		return
	}
	code, msg := ErrCodeLeaseExpired, "lease expired after the final attempt"
	t.ErrorCode = &code
	t.ErrorMessage = &msg
	t.Status = TaskFailed
	t.CompletedAt = &now
}

// ClaimedTask is the worker-facing projection of a leased task.
type ClaimedTask struct {
	TaskID         uuid.UUID
	JobID          uuid.UUID
	ChunkIndex     int
	Workload       string
	InputURI       string
	InputSHA256    string
	Parameters     map[string]any
	Attempt        int
	LeaseOwner     string
	LeaseExpiresAt time.Time
}

// ResultManifest is a completed task's output, ordered for the stitcher. It
// points at the coordinator-owned result artifact rather than a worker URI.
type ResultManifest struct {
	TaskID           uuid.UUID
	ChunkIndex       int
	ResultArtifactID uuid.UUID
	Metrics          map[string]any
}
