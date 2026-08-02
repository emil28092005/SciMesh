package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

type fakeAdminRead struct {
	jobs       []domain.Job
	taskCounts map[uuid.UUID]map[string]int
	sizes      map[string]int64
	byDay      map[string]int
	byWorkload map[string]int
	completed  int64
	failed     int64
	avg        float64
	dbSize     int64
}

func (f *fakeAdminRead) ListJobsPaginated(ctx context.Context, status string, limit, offset int) ([]domain.Job, int, error) {
	var out []domain.Job
	for _, j := range f.jobs {
		if status == "" || string(j.Status) == status {
			out = append(out, j)
		}
	}
	total := len(out)
	if offset >= len(out) {
		return nil, total, nil
	}
	if offset+limit < len(out) {
		out = out[offset : offset+limit]
	} else {
		out = out[offset:]
	}
	return out, total, nil
}

func (f *fakeAdminRead) CountJobsByStatus(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	for _, j := range f.jobs {
		out[string(j.Status)]++
	}
	return out, nil
}

func (f *fakeAdminRead) TaskCountsByJobs(ctx context.Context, jobIDs []uuid.UUID) (map[uuid.UUID]map[string]int, error) {
	return f.taskCounts, nil
}

func (f *fakeAdminRead) JobCountsByDay(ctx context.Context, since time.Time) (map[string]int, error) {
	return f.byDay, nil
}
func (f *fakeAdminRead) JobCountsByWorkload(ctx context.Context) (map[string]int, error) {
	return f.byWorkload, nil
}
func (f *fakeAdminRead) TaskStats(ctx context.Context) (int64, int64, float64, error) {
	return f.completed, f.failed, f.avg, nil
}
func (f *fakeAdminRead) ArtifactSizeByKind(ctx context.Context) (map[string]int64, error) {
	return f.sizes, nil
}
func (f *fakeAdminRead) DatabaseSizeBytes(ctx context.Context) (int64, error) { return f.dbSize, nil }

type fakeUIRead struct {
	UIReadRepository // embedded: only ListWorkers is exercised
	workers          []domain.Worker
}

func (f *fakeUIRead) ListWorkers(ctx context.Context, limit int) ([]domain.Worker, error) {
	return f.workers, nil
}

func adminFixture() *Admin {
	return NewAdmin(
		&fakeAdminRead{},
		&fakeUIRead{},
		AdminNodeInfo{
			Version: "1.1.0-alpha.1", StartedAt: time.Unix(1_000_000, 0).UTC(),
			Binary: "/usr/local/bin/coordinator", Addr: ":8080", DataDir: "/var/lib/scimesh",
			DBEngine: "sqlite", PublicURL: "http://192.168.1.10:8080", Userservice: "http://127.0.0.1:41273",
		},
		func(context.Context) error { return nil },
		func() time.Time { return time.Unix(1_000_000+3600*3, 0).UTC() },
	)
}

func TestAdminSystemAssemblesKpis(t *testing.T) {
	owner := uuid.New()
	a := adminFixture()
	a.read = &fakeAdminRead{
		jobs: []domain.Job{
			{ID: uuid.New(), Status: domain.JobRunning, Workload: "similarity-search"},
			{ID: uuid.New(), Status: domain.JobPending, Workload: "similarity-search", OwnerID: &owner},
			{ID: uuid.New(), Status: domain.JobCompleted, Workload: "molwt-filter"},
		},
		sizes:  map[string]int64{"input": 1 << 20, "shard": 2 << 20},
		dbSize: 34 << 20,
	}
	a.uiRead = &fakeUIRead{workers: []domain.Worker{
		{Status: domain.WorkerOnline},
		{Status: domain.WorkerBusy},
		{Status: domain.WorkerOffline},
	}}

	v, err := a.System(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != "1.1.0-alpha.1" {
		t.Errorf("version = %q", v.Version)
	}
	if v.ActiveJobs != 2 || v.WaitingJobs != 1 || v.RunningJobs != 1 {
		t.Errorf("jobs: active=%d waiting=%d running=%d, want 2/1/1", v.ActiveJobs, v.WaitingJobs, v.RunningJobs)
	}
	if v.WorkersOnline != 2 || v.WorkersBusy != 1 || v.WorkersTotal != 3 {
		t.Errorf("workers: online=%d busy=%d total=%d, want 2/1/3", v.WorkersOnline, v.WorkersBusy, v.WorkersTotal)
	}
	if v.UptimeSeconds != 10800 {
		t.Errorf("uptime = %d, want 10800", v.UptimeSeconds)
	}
	if v.Storage.DatasetsBytes != 1<<20 || v.Storage.ArtifactsBytes != 2<<20 || v.Storage.DatabaseBytes != 34<<20 {
		t.Errorf("storage = %+v", v.Storage)
	}
	if v.Health.Database != "connected" || v.Health.Userservice != "embedded" || v.Health.Reducer != "idle" {
		t.Errorf("health = %+v", v.Health)
	}
	if v.Node.Binary != "/usr/local/bin/coordinator" || v.Node.DBEngine != "sqlite" {
		t.Errorf("node = %+v", v.Node)
	}
}

func TestAdminSystemReportsUnhealthyDatabase(t *testing.T) {
	a := adminFixture()
	a.ready = func(context.Context) error { return errors.New("connection refused") }
	v, err := a.System(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.Health.Database != "error" {
		t.Errorf("database health = %q, want error", v.Health.Database)
	}
}

func TestAdminJobsDerivesStatusAndResolvesOwners(t *testing.T) {
	jobID := uuid.New()
	owner := uuid.New()
	a := adminFixture()
	a.read = &fakeAdminRead{
		jobs: []domain.Job{{ID: jobID, Status: domain.JobPending, Workload: "similarity-graph", OwnerID: &owner, CreatedAt: time.Unix(100, 0)}},
		taskCounts: map[uuid.UUID]map[string]int{
			jobID: {"completed": 5, "failed": 1, "running": 2},
		},
	}

	view, err := a.Jobs(context.Background(), "", 1, 20, map[uuid.UUID]string{owner: "alice@lab.org"})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(view.Jobs))
	}
	card := view.Jobs[0]
	if card.Owner != "alice@lab.org" || card.OwnerID != owner.String() {
		t.Errorf("owner = %q (%s)", card.Owner, card.OwnerID)
	}
	if card.Total != 8 || card.Completed != 5 || card.Failed != 1 {
		t.Errorf("progress: total=%d completed=%d failed=%d", card.Total, card.Completed, card.Failed)
	}
	if card.Status != "running" {
		t.Errorf("derived status = %q, want running (5 completed / 8 with 1 failed)", card.Status)
	}
	if view.Counts["pending"] != 1 {
		t.Errorf("counts = %v", view.Counts)
	}
}

func TestAdminJobsFallsBackWithoutOwners(t *testing.T) {
	jobID := uuid.New()
	a := adminFixture()
	a.read = &fakeAdminRead{jobs: []domain.Job{{ID: jobID, Status: domain.JobPending, Workload: "x", CreatedAt: time.Unix(100, 0)}}}
	view, err := a.Jobs(context.Background(), "", 1, 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if view.Jobs[0].Owner != "cluster token" {
		t.Errorf("owner fallback = %q, want cluster token", view.Jobs[0].Owner)
	}
}

func TestAdminMetricsBuckets(t *testing.T) {
	now := time.Unix(1_000_000+3600*3, 0).UTC()
	since := now.Add(-6 * 24 * time.Hour).Truncate(24 * time.Hour)
	day := func(offset int) string { return since.Add(time.Duration(offset) * 24 * time.Hour).Format("2006-01-02") }
	a := adminFixture()
	a.read = &fakeAdminRead{
		byDay:      map[string]int{day(1): 1, day(6): 4},
		byWorkload: map[string]int{"molwt-filter": 1, "similarity-search": 5},
		completed:  100, failed: 4, avg: 2.5,
	}
	v, err := a.Metrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.JobsByDay) != 7 || v.JobsLast7Days != 5 {
		t.Errorf("by day: %d entries, total %d (want 7 / 5)", len(v.JobsByDay), v.JobsLast7Days)
	}
	if v.JobsByDay[6].Count != 4 || v.JobsByDay[1].Count != 1 {
		t.Errorf("by day = %+v", v.JobsByDay)
	}
	if v.JobsByWorkload[0].Workload != "similarity-search" || v.JobsByWorkload[0].Count != 5 {
		t.Errorf("by workload = %+v", v.JobsByWorkload)
	}
	if v.FailureRate != 4.0/104.0 || v.AvgShardSeconds != 2.5 {
		t.Errorf("rate=%.4f avg=%.2f", v.FailureRate, v.AvgShardSeconds)
	}
}
