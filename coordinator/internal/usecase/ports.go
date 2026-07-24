// Package usecase holds the application's business operations. Each use case is
// a small type with its dependencies injected and a single Execute method.
//
// The interfaces below are *ports*: they are declared here, by the consumer,
// and implemented further out in storage/postgres. That is what keeps the
// dependency rule intact — usecase never imports storage or transport.
package usecase

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// ClaimFilter narrows which task a worker may be handed.
type ClaimFilter struct {
	Workloads  []string // workloads this worker can execute
	Owner      string   // worker ID taking the lease
	Now        time.Time
	LeaseUntil time.Time
}

// TaskRepository persists tasks.
//
// ClaimNext is deliberately coarse: leasing must be a single atomic statement
// (SELECT ... FOR UPDATE SKIP LOCKED + UPDATE), so it cannot be decomposed into
// Get+Update without losing the guarantee that one task goes to one worker.
type TaskRepository interface {
	// ClaimNext atomically leases one matching pending task.
	// Returns (nil, nil) when nothing is available.
	ClaimNext(ctx context.Context, f ClaimFilter) (*domain.Task, error)

	// Get reads a task without locking. Use it for read-only checks (e.g.
	// verifying lease ownership before a long upload) where holding a row lock
	// across the operation would be wrong.
	Get(ctx context.Context, id uuid.UUID) (*domain.Task, error)

	// GetForUpdate reads a task and locks its row for the enclosing
	// transaction, so read-modify-write use cases stay serialized.
	GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Task, error)

	// Update persists a mutated task, honouring its Version for optimistic
	// concurrency.
	Update(ctx context.Context, t *domain.Task) error

	InsertBatch(ctx context.Context, tasks []*domain.Task) error

	// ListCompleted returns completed tasks ordered by chunk_index.
	ListCompleted(ctx context.Context, jobID uuid.UUID) ([]*domain.Task, error)

	// CountByStatus aggregates a job's tasks for progress reporting.
	CountByStatus(ctx context.Context, jobID uuid.UUID) (map[domain.TaskStatus]int, error)

	// CancelByJob marks every non-terminal task as cancelled and invalidates its
	// lease. It returns how many tasks changed.
	CancelByJob(ctx context.Context, jobID uuid.UUID, now time.Time) (int64, error)

	// ExpireLeases applies the lease-expiry rule to every elapsed task and returns
	// the distinct jobs whose aggregate status may have changed.
	ExpireLeases(ctx context.Context, now time.Time) ([]uuid.UUID, error)
}

// JobRepository persists jobs.
type JobRepository interface {
	Insert(ctx context.Context, j *domain.Job) error
	Get(ctx context.Context, id uuid.UUID) (*domain.Job, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.JobStatus, completedAt *time.Time) error
}

// WorkerRepository persists the worker registry.
type WorkerRepository interface {
	Insert(ctx context.Context, w *domain.Worker) error
	Get(ctx context.Context, id uuid.UUID) (*domain.Worker, error)
	// Touch records liveness for a heartbeating worker, marking it online. A
	// no-op for an id that is not a registered worker.
	Touch(ctx context.Context, id uuid.UUID, at time.Time) error
	// MarkStaleOffline flips every worker last seen before cutoff to offline and
	// reports how many changed.
	MarkStaleOffline(ctx context.Context, cutoff time.Time) (int64, error)
}

// ArtifactRepository persists artifact metadata. The bytes live in a BlobStore;
// this keeps only the record that points at them.
type ArtifactRepository interface {
	Insert(ctx context.Context, a *domain.Artifact) error
	Get(ctx context.Context, id uuid.UUID) (*domain.Artifact, error)
	// FindPartialResult returns the durable result already uploaded for one task
	// attempt. A nil artifact means the attempt has not uploaded one yet.
	FindPartialResult(ctx context.Context, taskID uuid.UUID, attempt int) (*domain.Artifact, error)
}

// BlobStore holds artifact bytes, addressed by an opaque storage key. It streams
// in both directions so a large shard never has to sit in memory, and reports
// the checksum and size it measured while writing — the coordinator's own
// numbers, not the client's claim.
type BlobStore interface {
	Put(ctx context.Context, key string, r io.Reader) (sha256 string, size int64, err error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes a stored blob. Used to clean up after a metadata insert
	// fails, so a committed blob never outlives its (absent) record.
	Delete(ctx context.Context, key string) error
}

// TxManager runs a function inside one database transaction. The transaction
// travels in the context, so repositories pick it up without this port ever
// mentioning pgx.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Clock supplies the current time. Injecting it keeps lease and expiry rules
// testable without sleeping or freezing the system clock.
type Clock interface {
	Now() time.Time
}

// ErrNotImplemented marks scaffold code with no body yet. Unlike the errors in
// domain, it describes the state of this codebase, not a business rule.
var ErrNotImplemented = errors.New("not implemented")
