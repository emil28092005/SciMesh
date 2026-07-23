package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestNewJobWithTasksBuildsBoth(t *testing.T) {
	job, tasks, err := NewJobWithTasks("similarity_search", "s3://in", nil, []ChunkSpec{
		{ChunkIndex: 0, InputURI: "s3://c0", InputSHA256: "a"},
		{ChunkIndex: 1, InputURI: "s3://c1", InputSHA256: "b"},
	}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	for _, tk := range tasks {
		if tk.JobID != job.ID {
			t.Error("task not linked to job")
		}
		if tk.Workload != "similarity_search" {
			t.Error("task should inherit the job workload")
		}
	}
	if job.Status != JobPending {
		t.Errorf("status = %q, want pending", job.Status)
	}
}

func TestNewJobWithTasksRejectsBadInput(t *testing.T) {
	good := []ChunkSpec{{ChunkIndex: 0, InputURI: "s3://c0", InputSHA256: "a"}}
	cases := map[string]struct {
		workload string
		inputURI string
		chunks   []ChunkSpec
	}{
		"empty workload": {"", "s3://in", good},
		"empty input":    {"w", "", good},
		"no chunks":      {"w", "s3://in", nil},
		"duplicate index": {"w", "s3://in", []ChunkSpec{
			{ChunkIndex: 0, InputURI: "a", InputSHA256: "x"},
			{ChunkIndex: 0, InputURI: "b", InputSHA256: "y"},
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := NewJobWithTasks(c.workload, c.inputURI, nil, c.chunks, testNow); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestNewJobWithTasksInheritsAndOverridesWorkload(t *testing.T) {
	_, tasks, err := NewJobWithTasks("base", "s3://in", nil, []ChunkSpec{
		{ChunkIndex: 0, InputURI: "a", InputSHA256: "x"},
		{ChunkIndex: 1, InputURI: "b", InputSHA256: "y", Workload: "special"},
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].Workload != "base" || tasks[1].Workload != "special" {
		t.Errorf("workloads = %q, %q", tasks[0].Workload, tasks[1].Workload)
	}
}

func TestDeriveStatus(t *testing.T) {
	cases := []struct {
		name string
		p    JobProgress
		want JobStatus
	}{
		{"empty", JobProgress{Total: 0}, JobPending},
		{"all pending", JobProgress{Total: 3, Pending: 3}, JobPending},
		{"one leased", JobProgress{Total: 3, Pending: 2, Leased: 1}, JobRunning},
		{"partly done", JobProgress{Total: 3, Pending: 1, Done: 2}, JobRunning},
		{"all done", JobProgress{Total: 3, Done: 3}, JobCompleted},
		{"done and failed", JobProgress{Total: 3, Done: 2, Failed: 1}, JobFailed},
		{"failed but work remains", JobProgress{Total: 3, Pending: 1, Failed: 2}, JobRunning},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.DeriveStatus(); got != c.want {
				t.Errorf("DeriveStatus() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestNewUploadedJob(t *testing.T) {
	job, err := NewUploadedJob("w", map[string]any{"k": 1}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobPending || job.InputURI != "" {
		t.Error("uploaded job should be pending with no input URI")
	}
	if _, err := NewUploadedJob("", nil, testNow); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty workload: err = %v, want ErrInvalidInput", err)
	}
}

func TestNewShardTask(t *testing.T) {
	art := uuid.New()
	task, err := NewShardTask(uuid.New(), 2, "w", art, "sha", nil, 0, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if task.InputArtifactID == nil || *task.InputArtifactID != art {
		t.Error("shard task must reference its input artifact")
	}
	if task.InputURI != "" {
		t.Error("shard task must not carry a URI")
	}
	if task.MaxAttempts != DefaultMaxAttempts {
		t.Errorf("maxAttempts = %d, want default %d", task.MaxAttempts, DefaultMaxAttempts)
	}

	bad := []struct {
		name string
		art  uuid.UUID
		sha  string
		idx  int
	}{
		{"nil artifact", uuid.Nil, "sha", 0},
		{"empty sha", art, "", 0},
		{"negative index", art, "sha", -1},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewShardTask(uuid.New(), c.idx, "w", c.art, c.sha, nil, 0, testNow); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}
