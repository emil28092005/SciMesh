package domain

import (
	"time"

	"github.com/google/uuid"
)

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// Job is one user submission that fans out into one or more tasks.
type Job struct {
	ID          uuid.UUID
	Workload    string
	InputURI    string
	Parameters  map[string]any
	Status      JobStatus
	CreatedAt   time.Time
	CompletedAt *time.Time
}

// ChunkSpec describes one piece a job is split into. Callers build these from
// whatever chunking strategy the workload uses; the domain only validates them.
type ChunkSpec struct {
	ChunkIndex  int
	Workload    string // empty inherits the job's workload
	InputURI    string
	InputSHA256 string
	Parameters  map[string]any
	MaxAttempts int
}

// NewJobWithTasks builds a job together with all of its tasks, validating the
// set as a whole. Returning both from one constructor keeps the invariant
// visible: a job without tasks, or with duplicate chunk indexes, cannot exist.
func NewJobWithTasks(workload, inputURI string, params map[string]any,
	chunks []ChunkSpec, now time.Time) (*Job, []*Task, error) {

	if workload == "" || inputURI == "" || len(chunks) == 0 {
		return nil, nil, ErrInvalidInput
	}

	job := &Job{
		ID:         uuid.New(),
		Workload:   workload,
		InputURI:   inputURI,
		Parameters: params,
		Status:     JobPending,
		CreatedAt:  now,
	}

	seen := make(map[int]struct{}, len(chunks))
	tasks := make([]*Task, 0, len(chunks))
	for _, c := range chunks {
		if _, dup := seen[c.ChunkIndex]; dup {
			return nil, nil, ErrInvalidInput // unique (job_id, chunk_index)
		}
		seen[c.ChunkIndex] = struct{}{}

		w := c.Workload
		if w == "" {
			w = workload
		}
		task, err := NewTask(job.ID, c.ChunkIndex, w, c.InputURI, c.InputSHA256,
			c.Parameters, c.MaxAttempts, now)
		if err != nil {
			return nil, nil, err
		}
		tasks = append(tasks, task)
	}
	return job, tasks, nil
}

// JobProgress is the aggregate view of a job and the state of its tasks.
type JobProgress struct {
	Job     Job
	Total   int
	Pending int
	Leased  int
	Done    int
	Failed  int
}

// DeriveStatus computes what the job's status should be from its task counts,
// so the rule lives here rather than in a SQL trigger or a handler.
func (p JobProgress) DeriveStatus() JobStatus {
	switch {
	case p.Total == 0:
		return JobPending
	case p.Done == p.Total:
		return JobCompleted
	case p.Failed > 0 && p.Done+p.Failed == p.Total:
		return JobFailed
	case p.Leased > 0 || p.Done > 0 || p.Failed > 0:
		return JobRunning
	default:
		return JobPending
	}
}
