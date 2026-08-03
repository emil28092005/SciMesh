package usecase

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/workloads"
)

// AdminReadRepository is the bounded read projection behind the coordinator
// admin console. Like UIReadRepository it exposes no storage paths or
// credentials; unlike it, every method is admin-scoped (no owner filter).
type AdminReadRepository interface {
	// ListJobsPaginated returns one page of jobs filtered by stored status;
	// an empty status returns all. total counts the filtered set (for the
	// pager).
	ListJobsPaginated(ctx context.Context, status string, limit, offset int) (jobs []domain.Job, total int, err error)
	// CountJobsByStatus powers the status tabs: every stored status, all jobs.
	CountJobsByStatus(ctx context.Context) (map[string]int, error)
	// TaskCountsByJobs aggregates task statuses per job for progress bars.
	TaskCountsByJobs(ctx context.Context, jobIDs []uuid.UUID) (map[uuid.UUID]map[string]int, error)
	// JobCountsByDay buckets jobs created since `since` by UTC day
	// ("2006-01-02").
	JobCountsByDay(ctx context.Context, since time.Time) (map[string]int, error)
	// JobCountsByWorkload counts all jobs per workload name.
	JobCountsByWorkload(ctx context.Context) (map[string]int, error)
	// TaskStats totals shard execution: completed/failed counts and the mean
	// run duration of completed shards (seconds; 0 when nothing completed).
	TaskStats(ctx context.Context) (completed, failed int64, avgSeconds float64, err error)
	// ArtifactSizeByKind sums stored bytes per artifact kind.
	ArtifactSizeByKind(ctx context.Context) (map[string]int64, error)
	// DatabaseSizeBytes reports the engine's own size figure (sqlite pages,
	// pg_database_size); 0 when the engine cannot say.
	DatabaseSizeBytes(ctx context.Context) (int64, error)
}

// AdminNodeInfo describes the running coordinator process to the admin
// console. It is static for the process lifetime and assembled at startup.
type AdminNodeInfo struct {
	Version     string
	StartedAt   time.Time
	Binary      string
	Addr        string
	DataDir     string
	DBEngine    string
	PublicURL   string
	Userservice string // base URL; empty when the UI runs without user auth
	// WorkerToken reads the shared worker token for the Settings page. It is a
	// func so serve mode can read the token file lazily after provisioning.
	WorkerToken func() string
}

type AdminStorageView struct {
	DatasetsBytes  int64 `json:"datasets_bytes"`
	ArtifactsBytes int64 `json:"artifacts_bytes"`
	DatabaseBytes  int64 `json:"database_bytes"`
}

type AdminHealthView struct {
	Database    string `json:"database"`    // connected | error
	Reducer     string `json:"reducer"`     // idle | active
	Userservice string `json:"userservice"` // embedded | external | disabled
}

type AdminNodeView struct {
	Binary    string `json:"binary"`
	Addr      string `json:"addr"`
	DataDir   string `json:"data_dir"`
	DBEngine  string `json:"db_engine"`
	PublicURL string `json:"public_url"`
}

type AdminSystemView struct {
	Version       string           `json:"version"`
	StartedAt     time.Time        `json:"started_at"`
	UptimeSeconds int64            `json:"uptime_seconds"`
	ActiveJobs    int              `json:"active_jobs"`
	RunningJobs   int              `json:"running_jobs"`
	WaitingJobs   int              `json:"waiting_jobs"`
	WorkersOnline int              `json:"workers_online"`
	WorkersBusy   int              `json:"workers_busy"`
	WorkersTotal  int              `json:"workers_total"`
	Storage       AdminStorageView `json:"storage"`
	Health        AdminHealthView  `json:"health"`
	Node          AdminNodeView    `json:"node"`
}

// AdminJobCard is one row of the admin jobs table. Owner is a display string
// resolved by the caller (email when the userservice is reachable, a short id
// or "cluster token" otherwise).
type AdminJobCard struct {
	ID          string     `json:"id"`
	Workload    string     `json:"workload"`
	Status      string     `json:"status"`
	OwnerID     string     `json:"owner_id,omitempty"`
	Owner       string     `json:"owner"`
	Total       int        `json:"total"`
	Completed   int        `json:"completed"`
	Failed      int        `json:"failed"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type AdminJobsView struct {
	Jobs    []AdminJobCard `json:"jobs"`
	Total   int            `json:"total"`
	Page    int            `json:"page"`
	PerPage int            `json:"per_page"`
	// Counts holds every stored status for the filter tabs (all jobs, not
	// just the current filter).
	Counts map[string]int `json:"counts"`
}

type AdminDayCount struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

type AdminWorkloadCount struct {
	Workload string `json:"workload"`
	Count    int    `json:"count"`
}

type AdminMetricsView struct {
	JobsLast7Days   int                  `json:"jobs_last_7_days"`
	JobsByDay       []AdminDayCount      `json:"jobs_by_day"`
	JobsByWorkload  []AdminWorkloadCount `json:"jobs_by_workload"`
	ShardsCompleted int64                `json:"shards_completed"`
	ShardsFailed    int64                `json:"shards_failed"`
	AvgShardSeconds float64              `json:"avg_shard_seconds"`
	FailureRate     float64              `json:"failure_rate"`
}

// Admin answers the coordinator admin console from the bounded read model
// plus process info supplied at startup.
type Admin struct {
	read     AdminReadRepository
	uiRead   UIReadRepository
	workers  WorkerRepository
	settings WorkloadSettingsRepository
	catalog  *workloads.Catalog
	node     AdminNodeInfo
	ready    func(context.Context) error
	now      func() time.Time
	log      *slog.Logger
	audit    func(ctx context.Context, action, detail string)
}

func NewAdmin(read AdminReadRepository, uiRead UIReadRepository, workers WorkerRepository,
	settings WorkloadSettingsRepository, catalog *workloads.Catalog, node AdminNodeInfo,
	ready func(context.Context) error, now func() time.Time) *Admin {
	if now == nil {
		now = time.Now
	}
	return &Admin{read: read, uiRead: uiRead, workers: workers, settings: settings, catalog: catalog, node: node, ready: ready, now: now}
}

// WithAuditLog attaches an audit sink for sensitive actions (token reveal).
// Without it the admin usecase stays silent about them.
func (a *Admin) WithAuditLog(log *slog.Logger, audit func(ctx context.Context, action, detail string)) *Admin {
	a.log = log
	a.audit = audit
	return a
}

func (a *Admin) revealToken(ctx context.Context, actor string) string {
	if a.node.WorkerToken != nil {
		return a.node.WorkerToken()
	}
	return ""
}

func (a *Admin) System(ctx context.Context) (AdminSystemView, error) {
	counts, err := a.read.CountJobsByStatus(ctx)
	if err != nil {
		return AdminSystemView{}, err
	}
	workers, err := a.uiRead.ListWorkers(ctx, 100)
	if err != nil {
		return AdminSystemView{}, err
	}
	sizes, err := a.read.ArtifactSizeByKind(ctx)
	if err != nil {
		return AdminSystemView{}, err
	}
	dbSize, err := a.read.DatabaseSizeBytes(ctx)
	if err != nil {
		return AdminSystemView{}, err
	}

	out := AdminSystemView{
		Version:     a.node.Version,
		StartedAt:   a.node.StartedAt,
		WaitingJobs: counts[string(domain.JobPending)],
		RunningJobs: counts[string(domain.JobRunning)] + counts[string(domain.JobReducing)],
	}
	out.ActiveJobs = out.WaitingJobs + out.RunningJobs
	out.UptimeSeconds = int64(a.now().Sub(a.node.StartedAt).Seconds())
	if out.UptimeSeconds < 0 {
		out.UptimeSeconds = 0
	}
	for _, w := range workers {
		out.WorkersTotal++
		switch w.Status {
		case domain.WorkerOnline:
			out.WorkersOnline++
		case domain.WorkerBusy:
			out.WorkersOnline++
			out.WorkersBusy++
		}
	}
	for kind, size := range sizes {
		if kind == string(domain.ArtifactInput) {
			out.Storage.DatasetsBytes += size
		} else {
			out.Storage.ArtifactsBytes += size
		}
	}
	out.Storage.DatabaseBytes = dbSize

	out.Health.Database = "connected"
	if a.ready != nil {
		if err := a.ready(ctx); err != nil {
			out.Health.Database = "error"
		}
	}
	out.Health.Reducer = "idle"
	if counts[string(domain.JobReducing)] > 0 {
		out.Health.Reducer = "active"
	}
	out.Health.Userservice = "disabled"
	if a.node.Userservice != "" {
		out.Health.Userservice = "external"
		// The embedded userservice always binds the loopback interface.
		if strings.Contains(a.node.Userservice, "127.0.0.1") || strings.Contains(a.node.Userservice, "localhost") {
			out.Health.Userservice = "embedded"
		}
	}
	out.Node = AdminNodeView{
		Binary:    a.node.Binary,
		Addr:      a.node.Addr,
		DataDir:   a.node.DataDir,
		DBEngine:  a.node.DBEngine,
		PublicURL: a.node.PublicURL,
	}
	return out, nil
}

// Jobs returns one page of the admin jobs table. The owner emails map may be
// nil; cards then fall back to a short id or "cluster token".
func (a *Admin) Jobs(ctx context.Context, status string, page, perPage int, ownerEmails map[uuid.UUID]string) (AdminJobsView, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	jobs, total, err := a.read.ListJobsPaginated(ctx, status, perPage, (page-1)*perPage)
	if err != nil {
		return AdminJobsView{}, err
	}
	counts, err := a.read.CountJobsByStatus(ctx)
	if err != nil {
		return AdminJobsView{}, err
	}
	jobIDs := make([]uuid.UUID, 0, len(jobs))
	for _, job := range jobs {
		jobIDs = append(jobIDs, job.ID)
	}
	taskCounts, err := a.read.TaskCountsByJobs(ctx, jobIDs)
	if err != nil {
		return AdminJobsView{}, err
	}
	out := AdminJobsView{
		Jobs:    make([]AdminJobCard, 0, len(jobs)),
		Total:   total,
		Page:    page,
		PerPage: perPage,
		Counts:  counts,
	}
	for _, job := range jobs {
		tc := taskCounts[job.ID]
		card := AdminJobCard{
			ID:          job.ID.String(),
			Workload:    job.Workload,
			CreatedAt:   job.CreatedAt,
			CompletedAt: job.CompletedAt,
			Owner:       "cluster token",
		}
		var pending, leased, cancelled int
		for status, n := range tc {
			card.Total += n
			switch domain.TaskStatus(status) {
			case domain.TaskCompleted:
				card.Completed = n
			case domain.TaskFailed:
				card.Failed = n
			case domain.TaskPending:
				pending = n
			case domain.TaskLeased, domain.TaskRunning:
				leased += n
			case domain.TaskCancelled:
				cancelled = n
			}
		}
		// Derive the status exactly like the operator dashboard does, so the
		// two views never disagree about the same job.
		progress := domain.JobProgress{Job: job, Total: card.Total, Pending: pending, Leased: leased, Done: card.Completed, Failed: card.Failed, Cancelled: cancelled}
		card.Status = string(progress.DeriveStatus())
		if job.OwnerID != nil {
			card.OwnerID = job.OwnerID.String()
			card.Owner = "user " + shortID(job.OwnerID.String())
			if email, ok := ownerEmails[*job.OwnerID]; ok && email != "" {
				card.Owner = email
			}
		}
		out.Jobs = append(out.Jobs, card)
	}
	return out, nil
}

func (a *Admin) Metrics(ctx context.Context) (AdminMetricsView, error) {
	since := a.now().Add(-6 * 24 * time.Hour).Truncate(24 * time.Hour)
	byDay, err := a.read.JobCountsByDay(ctx, since)
	if err != nil {
		return AdminMetricsView{}, err
	}
	byWorkload, err := a.read.JobCountsByWorkload(ctx)
	if err != nil {
		return AdminMetricsView{}, err
	}
	completed, failed, avg, err := a.read.TaskStats(ctx)
	if err != nil {
		return AdminMetricsView{}, err
	}
	out := AdminMetricsView{
		JobsByDay:       make([]AdminDayCount, 0, 7),
		JobsByWorkload:  make([]AdminWorkloadCount, 0, len(byWorkload)),
		ShardsCompleted: completed,
		ShardsFailed:    failed,
		AvgShardSeconds: avg,
	}
	if completed+failed > 0 {
		out.FailureRate = float64(failed) / float64(completed+failed)
	}
	for i := 0; i < 7; i++ {
		day := since.Add(time.Duration(i) * 24 * time.Hour).UTC().Format("2006-01-02")
		count := byDay[day]
		out.JobsByDay = append(out.JobsByDay, AdminDayCount{Day: day, Count: count})
		out.JobsLast7Days += count
	}
	for workload, count := range byWorkload {
		out.JobsByWorkload = append(out.JobsByWorkload, AdminWorkloadCount{Workload: workload, Count: count})
	}
	sort.Slice(out.JobsByWorkload, func(i, j int) bool {
		if out.JobsByWorkload[i].Count != out.JobsByWorkload[j].Count {
			return out.JobsByWorkload[i].Count > out.JobsByWorkload[j].Count
		}
		return out.JobsByWorkload[i].Workload < out.JobsByWorkload[j].Workload
	})
	return out, nil
}

// AdminWorkerCard is one row of the admin workers table.
type AdminWorkerCard struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Status          string    `json:"status"`
	Capabilities    []string  `json:"capabilities"`
	Trust           string    `json:"trust"`
	OwnerID         string    `json:"owner_id,omitempty"`
	Owner           string    `json:"owner"`
	Completed       int       `json:"completed"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
}

type AdminWorkersView struct {
	Workers []AdminWorkerCard `json:"workers"`
}

// Workers lists the whole fleet for the admin console. Owner emails are
// resolved through the same map as the jobs table (userservice-backed).
func (a *Admin) Workers(ctx context.Context, ownerEmails map[uuid.UUID]string) (AdminWorkersView, error) {
	workers, err := a.uiRead.ListWorkers(ctx, 100)
	if err != nil {
		return AdminWorkersView{}, err
	}
	out := AdminWorkersView{Workers: make([]AdminWorkerCard, 0, len(workers))}
	for _, w := range workers {
		card := AdminWorkerCard{
			ID:              w.ID.String(),
			Name:            w.Name,
			Status:          string(w.Status),
			Capabilities:    w.Capabilities,
			Trust:           string(w.TrustLevel),
			LastHeartbeatAt: w.LastHeartbeatAt,
			Owner:           "cluster token",
		}
		if w.OwnerID != nil {
			card.OwnerID = w.OwnerID.String()
			card.Owner = "user " + shortID(w.OwnerID.String())
			if email, ok := ownerEmails[*w.OwnerID]; ok && email != "" {
				card.Owner = email
			}
		}
		out.Workers = append(out.Workers, card)
	}
	return out, nil
}

// SetTrust reclassifies one worker (trusted/untrusted).
func (a *Admin) SetTrust(ctx context.Context, id uuid.UUID, trusted bool) error {
	trust := domain.WorkerUntrusted
	if trusted {
		trust = domain.WorkerTrusted
	}
	return a.workers.SetTrust(ctx, id, trust)
}

// AdminWorkloadView is the catalog plus the persisted enable flag.
type AdminWorkloadView struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Reduction   string     `json:"reduction"`
	Parameters  int        `json:"parameters"`
	UploadReady bool       `json:"upload_ready"`
	Enabled     bool       `json:"enabled"`
	DefaultOn   bool       `json:"default_on"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

type AdminWorkloadsView struct {
	Workloads []AdminWorkloadView `json:"workloads"`
}

// Workloads lists the catalog with persisted enable/disable overrides.
func (a *Admin) Workloads(ctx context.Context) (AdminWorkloadsView, error) {
	if a.catalog == nil {
		return AdminWorkloadsView{}, domain.ErrInvalidInput
	}
	items := a.catalog.Items()
	overrides, err := a.settings.List(ctx)
	if err != nil {
		return AdminWorkloadsView{}, err
	}
	enabled := make(map[string]WorkloadSetting, len(overrides))
	for _, s := range overrides {
		enabled[s.Workload] = s
	}
	out := AdminWorkloadsView{Workloads: make([]AdminWorkloadView, 0, len(items))}
	for _, item := range items {
		params := 0
		if properties, ok := item.Parameters["properties"].(map[string]any); ok {
			params = len(properties)
		}
		view := AdminWorkloadView{
			Name:        item.Name,
			Description: item.Description,
			Reduction:   item.Reduction,
			Parameters:  params,
			UploadReady: item.UploadReady,
			Enabled:     true,
			DefaultOn:   true,
		}
		if s, ok := enabled[item.Name]; ok {
			view.Enabled = s.Enabled
			view.DefaultOn = false
			view.UpdatedAt = &s.UpdatedAt
		}
		out.Workloads = append(out.Workloads, view)
	}
	return out, nil
}

// SetWorkloadEnabled flips the persisted enable flag. An unknown workload is
// rejected: the admin console must not invent catalog entries.
func (a *Admin) SetWorkloadEnabled(ctx context.Context, name string, enabled bool) error {
	if a.catalog == nil || a.catalog.ByName(name) == nil {
		return domain.ErrInvalidInput
	}
	return a.settings.SetEnabled(ctx, name, enabled, a.now())
}

// AdminSettingsView is the read-only cluster configuration the Settings page
// shows. The token is never included; it is revealed only through
// RevealWorkerToken, which audits.
type AdminSettingsView struct {
	PublicURL string `json:"public_url"`
	Addr      string `json:"addr"`
	DataDir   string `json:"data_dir"`
	DBEngine  string `json:"db_engine"`
	Binary    string `json:"binary"`
}

func (a *Admin) Settings() AdminSettingsView {
	return AdminSettingsView{
		PublicURL: a.node.PublicURL,
		Addr:      a.node.Addr,
		DataDir:   a.node.DataDir,
		DBEngine:  a.node.DBEngine,
		Binary:    a.node.Binary,
	}
}

// RevealWorkerToken returns the shared worker token for the Settings page and
// records the reveal in the audit log. It must only be called for an admin
// session.
func (a *Admin) RevealWorkerToken(ctx context.Context, actor string) string {
	token := a.revealToken(ctx, actor)
	if a.audit != nil {
		a.audit(ctx, "worker token revealed", "by "+actor)
	}
	if a.log != nil {
		a.log.Warn("admin console revealed the worker token", "actor", actor)
	}
	return token
}

// RemoveWorker deletes an offline worker from the registry. Online or busy
// workers are refused: an admin console must never yank a live machine out
// from under a running task.
func (a *Admin) RemoveWorker(ctx context.Context, id uuid.UUID) error {
	if a.workers == nil {
		return domain.ErrWorkerNotFound
	}
	worker, err := a.workers.Get(ctx, id)
	if err != nil {
		return err
	}
	if worker.Status != domain.WorkerOffline {
		return domain.ErrInvalidInput
	}
	return a.workers.Delete(ctx, id)
}
