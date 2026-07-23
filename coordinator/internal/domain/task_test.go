package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	testNow       = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	testLater     = testNow.Add(time.Hour)
	testWorker    = "worker-1"
	testResult    = uuid.New()
	testResultAlt = uuid.New()
)

// leasedTask builds a task already leased to testWorker at the given attempt.
func leasedTask(attempt, maxAttempts int) *Task {
	owner := testWorker
	expires := testLater
	return &Task{
		ID:             uuid.New(),
		JobID:          uuid.New(),
		Status:         TaskLeased,
		Attempt:        attempt,
		MaxAttempts:    maxAttempts,
		LeaseOwner:     &owner,
		LeaseExpiresAt: &expires,
	}
}

func TestCompleteWithRecordsResult(t *testing.T) {
	task := leasedTask(1, 3)

	if err := task.CompleteWith(testResult, nil, testWorker, 1, testNow); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Status != TaskCompleted {
		t.Errorf("status = %q, want completed", task.Status)
	}
	if task.LeaseOwner != nil || task.LeaseExpiresAt != nil {
		t.Error("lease must be released on completion")
	}
	if task.CompletedAt == nil || !task.CompletedAt.Equal(testNow) {
		t.Error("completed_at must be stamped")
	}
}

// A worker whose network dropped retries the same manifest; that must succeed
// rather than fail on the lease it has already given up.
func TestCompleteWithIsIdempotentForSameManifest(t *testing.T) {
	task := leasedTask(1, 3)
	if err := task.CompleteWith(testResult, nil, testWorker, 1, testNow); err != nil {
		t.Fatalf("first call: %v", err)
	}
	versionAfterFirst := task.Version

	if err := task.CompleteWith(testResult, nil, testWorker, 1, testLater); err != nil {
		t.Fatalf("replay must be idempotent, got %v", err)
	}
	if task.Version != versionAfterFirst {
		t.Error("replay must not mutate the task")
	}
}

func TestCompleteWithRejectsDifferentManifest(t *testing.T) {
	task := leasedTask(1, 3)
	if err := task.CompleteWith(testResult, nil, testWorker, 1, testNow); err != nil {
		t.Fatalf("first call: %v", err)
	}

	err := task.CompleteWith(testResultAlt, nil, testWorker, 1, testLater)
	if !errors.Is(err, ErrResultConflict) {
		t.Errorf("err = %v, want ErrResultConflict", err)
	}
}

func TestCompleteWithRejectsForeignWorker(t *testing.T) {
	task := leasedTask(1, 3)

	err := task.CompleteWith(testResult, nil, "worker-2", 1, testNow)
	if !errors.Is(err, ErrLeaseConflict) {
		t.Errorf("err = %v, want ErrLeaseConflict", err)
	}
}

func TestCompleteWithRejectsStaleAttempt(t *testing.T) {
	task := leasedTask(2, 3) // task is on attempt 2

	err := task.CompleteWith(testResult, nil, testWorker, 1, testNow) // worker thinks it is 1
	if !errors.Is(err, ErrStaleAttempt) {
		t.Errorf("err = %v, want ErrStaleAttempt", err)
	}
}

func TestFailRequeuesWhileAttemptsRemain(t *testing.T) {
	task := leasedTask(1, 3)

	if err := task.Fail(testWorker, 1, "boom", "exploded", true, testNow); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Status != TaskPending {
		t.Errorf("status = %q, want pending", task.Status)
	}
	if task.LeaseOwner != nil {
		t.Error("lease must be released so another worker can claim it")
	}
}

func TestFailTerminatesOnFinalAttempt(t *testing.T) {
	task := leasedTask(3, 3) // no attempts left

	if err := task.Fail(testWorker, 3, "boom", "exploded", true, testNow); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Status != TaskFailed {
		t.Errorf("status = %q, want failed", task.Status)
	}
}

func TestFailIsTerminalWhenNotRetryable(t *testing.T) {
	task := leasedTask(1, 3) // attempts remain, but the error is fatal

	if err := task.Fail(testWorker, 1, "bad_input", "checksum mismatch", false, testNow); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Status != TaskFailed {
		t.Errorf("status = %q, want failed", task.Status)
	}
}

// This is the MVP acceptance criterion: a dead worker must not strand its task.
func TestExpireLeaseRequeuesWhileAttemptsRemain(t *testing.T) {
	task := leasedTask(1, 3)

	task.ExpireLease(testNow)

	if task.Status != TaskPending {
		t.Errorf("status = %q, want pending", task.Status)
	}
	if task.LeaseOwner != nil || task.LeaseExpiresAt != nil {
		t.Error("expired lease must be cleared")
	}
}

func TestExpireLeaseFailsAfterFinalAttempt(t *testing.T) {
	task := leasedTask(3, 3)

	task.ExpireLease(testNow)

	if task.Status != TaskFailed {
		t.Errorf("status = %q, want failed", task.Status)
	}
	if task.ErrorCode == nil || *task.ErrorCode != ErrCodeLeaseExpired {
		t.Error("expected a lease_expired error code")
	}
}

func TestExpireLeaseIgnoresUnleasedTasks(t *testing.T) {
	task := &Task{Status: TaskCompleted, Attempt: 1, MaxAttempts: 3}

	task.ExpireLease(testNow)

	if task.Status != TaskCompleted {
		t.Errorf("status = %q, completed tasks must be untouched", task.Status)
	}
}

func TestFirstHeartbeatMovesLeasedToRunning(t *testing.T) {
	task := leasedTask(1, 3)
	until := testLater.Add(time.Hour)

	if err := task.RenewLease(testWorker, 1, until); err != nil {
		t.Fatal(err)
	}
	if task.Status != TaskRunning {
		t.Errorf("status = %q, want running after first heartbeat", task.Status)
	}
	// A second heartbeat keeps it running.
	if err := task.RenewLease(testWorker, 1, until); err != nil {
		t.Fatal(err)
	}
	if task.Status != TaskRunning {
		t.Errorf("status = %q, want running", task.Status)
	}
}

func TestRunningTaskCanBeCompletedAndExpired(t *testing.T) {
	// Complete works from running.
	task := leasedTask(1, 3)
	_ = task.RenewLease(testWorker, 1, testLater) // -> running
	if err := task.CompleteWith(testResult, nil, testWorker, 1, testNow); err != nil {
		t.Errorf("complete from running: %v", err)
	}

	// Expire reclaims a running task too.
	task2 := leasedTask(1, 3)
	_ = task2.RenewLease(testWorker, 1, testLater) // -> running
	task2.ExpireLease(testNow)
	if task2.Status != TaskPending {
		t.Errorf("status = %q, want pending after a running lease expires", task2.Status)
	}
}

func TestRenewLeaseExtendsOnlyForHolder(t *testing.T) {
	task := leasedTask(1, 3)
	until := testLater.Add(time.Hour)

	if err := task.RenewLease(testWorker, 1, until); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !task.LeaseExpiresAt.Equal(until) {
		t.Error("lease must be extended")
	}

	if err := task.RenewLease("worker-2", 1, until); !errors.Is(err, ErrLeaseConflict) {
		t.Errorf("err = %v, want ErrLeaseConflict", err)
	}
}
