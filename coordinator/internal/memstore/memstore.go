// Package memstore holds in-memory implementations of the usecase ports for
// tests: they exercise use-case orchestration without a database or filesystem.
// The real invariants that depend on Postgres (SKIP LOCKED, row locking) are
// covered separately by the integration tests.
package memstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// Clock returns a fixed, advanceable time.
type Clock struct{ t time.Time }

func NewClock(t time.Time) *Clock        { return &Clock{t: t} }
func (c *Clock) Now() time.Time          { return c.t }
func (c *Clock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// Tx is a no-op transaction manager: the in-memory stores need no atomicity to
// be observed, so it simply runs the function.
type Tx struct{}

func (Tx) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

// --- TaskRepo ------------------------------------------------------------

type TaskRepo struct {
	mu    sync.Mutex
	tasks map[uuid.UUID]*domain.Task
}

func NewTaskRepo() *TaskRepo { return &TaskRepo{tasks: map[uuid.UUID]*domain.Task{}} }

var _ usecase.TaskRepository = (*TaskRepo)(nil)

// clone returns a copy so a caller's mutations do not touch stored state until
// Update — mirroring how a repository hands back detached entities.
func clone(t *domain.Task) *domain.Task { cp := *t; return &cp }

func (r *TaskRepo) put(t *domain.Task) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[t.ID] = clone(t)
}

func (r *TaskRepo) ClaimNext(ctx context.Context, f usecase.ClaimFilter) (*domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var cands []*domain.Task
	for _, t := range r.tasks {
		if t.Status != domain.TaskPending || t.Attempt >= t.MaxAttempts {
			continue
		}
		if len(f.Workloads) > 0 && !contains(f.Workloads, t.Workload) {
			continue
		}
		cands = append(cands, t)
	}
	if len(cands) == 0 {
		return nil, nil
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].CreatedAt.Equal(cands[j].CreatedAt) {
			return cands[i].ChunkIndex < cands[j].ChunkIndex
		}
		return cands[i].CreatedAt.Before(cands[j].CreatedAt)
	})

	t := cands[0]
	t.Status = domain.TaskLeased
	t.Attempt++
	owner := f.Owner
	t.LeaseOwner = &owner
	t.LeaseExpiresAt = &f.LeaseUntil
	if t.StartedAt == nil {
		t.StartedAt = &f.Now
	}
	t.Version++
	return clone(t), nil
}

func (r *TaskRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[id]
	if !ok {
		return nil, domain.ErrTaskNotFound
	}
	return clone(t), nil
}

func (r *TaskRepo) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	return r.Get(ctx, id)
}

func (r *TaskRepo) Update(ctx context.Context, t *domain.Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.tasks[t.ID]
	if !ok || stored.Version != t.Version-1 {
		return domain.ErrLeaseConflict // vanished or advanced under us
	}
	r.tasks[t.ID] = clone(t)
	return nil
}

func (r *TaskRepo) InsertBatch(ctx context.Context, tasks []*domain.Task) error {
	for _, t := range tasks {
		r.put(t)
	}
	return nil
}

func (r *TaskRepo) ListCompleted(ctx context.Context, jobID uuid.UUID) ([]*domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Task
	for _, t := range r.tasks {
		if t.JobID == jobID && t.Status == domain.TaskCompleted {
			out = append(out, clone(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChunkIndex < out[j].ChunkIndex })
	return out, nil
}

func (r *TaskRepo) CountByStatus(ctx context.Context, jobID uuid.UUID) (map[domain.TaskStatus]int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := map[domain.TaskStatus]int{}
	for _, t := range r.tasks {
		if t.JobID == jobID {
			counts[t.Status]++
		}
	}
	return counts, nil
}

func (r *TaskRepo) CancelByJob(_ context.Context, jobID uuid.UUID, now time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var cancelled int64
	for _, task := range r.tasks {
		if task.JobID == jobID && task.Cancel(now) {
			cancelled++
		}
	}
	return cancelled, nil
}

func (r *TaskRepo) ExpireLeases(ctx context.Context, now time.Time) ([]uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	affected := make([]uuid.UUID, 0)
	for _, t := range r.tasks {
		if (t.Status == domain.TaskLeased || t.Status == domain.TaskRunning) &&
			t.LeaseExpiresAt != nil && t.LeaseExpiresAt.Before(now) {
			t.ExpireLease(now)
			affected = append(affected, t.JobID)
		}
	}
	return affected, nil
}

// --- JobRepo -------------------------------------------------------------

type JobRepo struct {
	mu   sync.Mutex
	jobs map[uuid.UUID]*domain.Job
}

func NewJobRepo() *JobRepo { return &JobRepo{jobs: map[uuid.UUID]*domain.Job{}} }

var _ usecase.JobRepository = (*JobRepo)(nil)

func (r *JobRepo) Insert(ctx context.Context, j *domain.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *j
	r.jobs[j.ID] = &cp
	return nil
}

func (r *JobRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return nil, domain.ErrJobNotFound
	}
	cp := *j
	return &cp, nil
}

func (r *JobRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.JobStatus, completedAt *time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return domain.ErrJobNotFound
	}
	j.Status = status
	j.CompletedAt = completedAt
	return nil
}

// --- WorkerRepo ----------------------------------------------------------

type WorkerRepo struct {
	mu      sync.Mutex
	workers map[uuid.UUID]*domain.Worker
}

func NewWorkerRepo() *WorkerRepo { return &WorkerRepo{workers: map[uuid.UUID]*domain.Worker{}} }

var _ usecase.WorkerRepository = (*WorkerRepo)(nil)

func (r *WorkerRepo) Insert(ctx context.Context, w *domain.Worker) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *w
	r.workers[w.ID] = &cp
	return nil
}

func (r *WorkerRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Worker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[id]
	if !ok {
		return nil, domain.ErrWorkerNotFound
	}
	cp := *w
	return &cp, nil
}

func (r *WorkerRepo) Touch(ctx context.Context, id uuid.UUID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.workers[id]; ok {
		w.LastHeartbeatAt = at
		w.Status = domain.WorkerOnline
	}
	return nil
}

func (r *WorkerRepo) MarkStaleOffline(ctx context.Context, cutoff time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for _, w := range r.workers {
		if w.Status != domain.WorkerOffline && w.LastHeartbeatAt.Before(cutoff) {
			w.Status = domain.WorkerOffline
			n++
		}
	}
	return n, nil
}

// --- ArtifactRepo --------------------------------------------------------

type ArtifactRepo struct {
	mu   sync.Mutex
	arts map[uuid.UUID]*domain.Artifact
}

func NewArtifactRepo() *ArtifactRepo { return &ArtifactRepo{arts: map[uuid.UUID]*domain.Artifact{}} }

var _ usecase.ArtifactRepository = (*ArtifactRepo)(nil)

func (r *ArtifactRepo) Insert(ctx context.Context, a *domain.Artifact) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *a
	r.arts[a.ID] = &cp
	return nil
}

func (r *ArtifactRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.arts[id]
	if !ok {
		return nil, domain.ErrArtifactNotFound
	}
	cp := *a
	return &cp, nil
}

func (r *ArtifactRepo) FindPartialResult(_ context.Context, taskID uuid.UUID, attempt int) (*domain.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.arts {
		if a.TaskID != nil && *a.TaskID == taskID && a.Kind == domain.ArtifactPartialResult &&
			a.Attempt != nil && *a.Attempt == attempt {
			return cloneArtifact(a), nil
		}
	}
	return nil, nil
}

func cloneArtifact(a *domain.Artifact) *domain.Artifact {
	cp := *a
	return &cp
}

// --- BlobStore -----------------------------------------------------------

type BlobStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func NewBlobStore() *BlobStore { return &BlobStore{blobs: map[string][]byte{}} }

var _ usecase.BlobStore = (*BlobStore)(nil)

func (b *BlobStore) Put(ctx context.Context, key string, r io.Reader) (string, int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	b.mu.Lock()
	b.blobs[key] = data
	b.mu.Unlock()
	return hex.EncodeToString(sum[:]), int64(len(data)), nil
}

func (b *BlobStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.blobs[key]
	if !ok {
		return nil, domain.ErrArtifactNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *BlobStore) Delete(ctx context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.blobs, key)
	return nil
}

// Has reports whether a blob exists — handy for asserting cleanup in tests.
func (b *BlobStore) Has(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.blobs[key]
	return ok
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
