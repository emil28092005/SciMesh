package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/emil28092005/SciMesh/coordinator/internal/infra"
	"github.com/emil28092005/SciMesh/coordinator/internal/metrics"
	"github.com/emil28092005/SciMesh/coordinator/internal/storage/blob"
	"github.com/emil28092005/SciMesh/coordinator/internal/storage/postgres"
	"github.com/emil28092005/SciMesh/coordinator/internal/storage/sqlite"
	httptransport "github.com/emil28092005/SciMesh/coordinator/internal/transport/http"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
	"github.com/emil28092005/SciMesh/coordinator/internal/workloads"
)

// version is injected at build time (-ldflags "-X main.version=...") and
// reported by --version. "dev" marks a local build.
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "setup":
			if err := runSetup(args[1:]); err != nil {
				fmt.Fprintln(os.Stderr, "setup:", err)
				os.Exit(1)
			}
			return
		case "serve":
			if err := runServe(args[1:]); err != nil {
				fmt.Fprintln(os.Stderr, "serve:", err)
				os.Exit(1)
			}
			return
		case "agent":
			if err := runAgent(args[1:]); err != nil {
				fmt.Fprintln(os.Stderr, "agent:", err)
				os.Exit(1)
			}
			return
		case "token":
			if err := runToken(args[1:]); err != nil {
				fmt.Fprintln(os.Stderr, "token:", err)
				os.Exit(1)
			}
			return
		}
	}
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("coordinator " + version)
		return
	}
	// All work happens in run() so its defers (pool.Close, log flush, signal
	// stop) still execute: os.Exit skips deferred calls entirely.
	if err := run(); err != nil {
		os.Exit(1)
	}
}

// storageDeps carries the engine-specific database handles and the repository
// implementations. The usecases below only ever see the ports.
type storageDeps struct {
	tx             usecase.TxManager
	taskRepo       usecase.TaskRepository
	jobRepo        usecase.JobRepository
	workerRepo     usecase.WorkerRepository
	artifactRepo   usecase.ArtifactRepository
	uiReadRepo     usecase.UIReadRepository
	adminReadRepo  usecase.AdminReadRepository
	settingsRepo   usecase.WorkloadSettingsRepository
	taskResultRepo usecase.TaskResultRepository
	statsRepo      interface {
		Counts(ctx context.Context) (tasks, jobs, workers map[string]int, err error)
	}
	ready   func(ctx context.Context) error
	migrate func(ctx context.Context, log *slog.Logger) error
	close   func()
}

func run() error {
	boot := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := infra.LoadConfig()
	if err != nil {
		boot.Error("load config", "err", err)
		return err
	}
	return runWithConfig(cfg)
}

// runWithConfig boots the coordinator server with an explicit config. The
// `serve` subcommand builds such a config for the single-binary mode; the
// plain `coordinator` binary loads it from the environment.
func runWithConfig(cfg infra.Config) error {
	// Bootstrap logger, used only until config says where logs should go. It
	// writes to stderr so it never contaminates the configured stdout stream.
	boot := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	// The real logger: stdout plus an optional rotated file (LOG_FILE).
	log, logCloser, err := infra.NewLogger(cfg)
	if err != nil {
		boot.Error("init logger", "err", err)
		return err
	}
	defer func() { _ = logCloser.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var deps *storageDeps
	switch cfg.DatabaseEngine {
	case "sqlite":
		deps, err = openSQLite(ctx, cfg, log)
	case "postgres":
		deps, err = openPostgres(ctx, cfg, log)
	default:
		err = fmt.Errorf("SCIMESH_DB must be sqlite or postgres")
	}
	if err != nil {
		log.Error("init storage", "err", err)
		return err
	}
	defer deps.close()

	// A downloaded binary provisions its own schema; AUTO_MIGRATE=false keeps
	// out-of-band migration workflows (the migrate CLI, CI, managed databases).
	if cfg.AutoMigrate {
		if err := deps.migrate(ctx, log); err != nil {
			log.Error("apply migrations", "err", err)
			return err
		}
	}

	blobStore, err := blob.NewFSStore(cfg.StorageDir)
	if err != nil {
		log.Error("init blob storage", "err", err)
		return err
	}

	clk := infra.NewClock()
	tx, taskRepo, jobRepo, workerRepo, artifactRepo, uiReadRepo, taskResultRepo :=
		deps.tx, deps.taskRepo, deps.jobRepo, deps.workerRepo, deps.artifactRepo, deps.uiReadRepo, deps.taskResultRepo

	catalog, err := workloads.Load()
	if err != nil {
		log.Error("load workload catalog", "err", err)
		return err
	}

	useCases := httptransport.UseCases{
		RegisterWorker:   usecase.NewRegisterWorker(workerRepo, clk),
		CreateJob:        usecase.NewCreateJob(jobRepo, taskRepo, tx, clk),
		SubmitDataset:    usecase.NewSubmitDataset(blobStore, artifactRepo, jobRepo, taskRepo, tx, clk, cfg.DefaultMaxAttempts, catalog, deps.settingsRepo),
		ClaimTask:        usecase.NewClaimTask(taskRepo, jobRepo, workerRepo, tx, clk, cfg.LeaseDuration, catalog),
		RenewLease:       usecase.NewRenewLease(taskRepo, workerRepo, tx, clk, cfg.LeaseDuration),
		CompleteTask:     usecase.NewCompleteTask(taskRepo, jobRepo, artifactRepo, workerRepo, taskResultRepo, tx, clk, cfg.QuorumSize, catalog),
		ReduceJob:        usecase.NewReduceJob(jobRepo, taskRepo, artifactRepo, blobStore, tx, clk, catalog),
		FailTask:         usecase.NewFailTask(taskRepo, jobRepo, workerRepo, tx, clk, catalog),
		GetJobStatus:     usecase.NewGetJobStatus(jobRepo, taskRepo),
		CancelJob:        usecase.NewCancelJob(jobRepo, taskRepo, tx, clk),
		UploadArtifact:   usecase.NewUploadArtifact(taskRepo, workerRepo, artifactRepo, blobStore, tx, clk),
		DownloadArtifact: usecase.NewDownloadArtifact(artifactRepo, blobStore),
		GetJobResult:     usecase.NewGetJobResult(jobRepo, usecase.NewDownloadArtifact(artifactRepo, blobStore)),
		GetTaskInput:     usecase.NewGetTaskInput(taskRepo, artifactRepo, blobStore),
		Dashboard:        usecase.NewDashboard(uiReadRepo, catalog),
		PruneArtifacts:   usecase.NewPruneArtifacts(jobRepo, uiReadRepo, blobStore, clk),
		PreviewArtifact:  usecase.NewPreviewArtifact(uiReadRepo, blobStore),
		Admin: usecase.NewAdmin(deps.adminReadRepo, uiReadRepo, workerRepo, deps.settingsRepo, catalog,
			usecase.AdminNodeInfo{
				Version:     version,
				StartedAt:   clk.Now(),
				Binary:      executablePath(),
				Addr:        cfg.Addr,
				DataDir:     cfg.StorageDir,
				DBEngine:    cfg.DatabaseEngine,
				PublicURL:   cfg.PublicCoordinatorURL,
				Userservice: cfg.UserserviceURL,
				WorkerToken: func() string { return cfg.Token },
			}, deps.ready, clk.Now).
			WithAuditLog(log, func(ctx context.Context, action, detail string) {
				log.Info("admin audit", "action", action, "detail", detail)
			}),
	}

	// Background reapers are tracked so shutdown can wait for them. Without this
	// the process would exit mid-UPDATE, and the deferred close() would pull
	// connections out from under them.
	expireLeases := usecase.NewExpireLeases(taskRepo, jobRepo, tx, clk, catalog)
	markOffline := usecase.NewMarkWorkersOffline(workerRepo, clk, cfg.WorkerOfflineAfter)

	var wg sync.WaitGroup
	for _, r := range []struct {
		name string
		fn   func(context.Context) (int64, error)
	}{
		{"reaper requeued expired leases", expireLeases.Execute},
		{"reaper marked workers offline", markOffline.Execute},
	} {
		wg.Add(1)
		go func(name string, fn func(context.Context) (int64, error)) {
			defer wg.Done()
			infra.RunPeriodic(ctx, log, name, cfg.ReaperInterval, fn)
		}(r.name, r.fn)
	}

	// Business metrics: gauges of tasks/jobs/workers by status, sampled from the
	// database on every Prometheus scrape.
	m := metrics.New()
	m.RegisterBusiness(func(ctx context.Context) (metrics.Stats, error) {
		tasks, jobs, workers, err := deps.statsRepo.Counts(ctx)
		return metrics.Stats{Tasks: tasks, Jobs: jobs, Workers: workers}, err
	})

	// deps.ready backs /health: readiness means the database answers, not just
	// that the process is alive.
	api := httptransport.NewServer(useCases, log, cfg.RequestTimeout, cfg.HeartbeatInterval, cfg.MaxUploadBytes, cfg.JWTSecret, cfg.UserserviceURL, m, deps.ready, cfg.PublicCoordinatorURL, cfg.PublicUserserviceURL, cfg.DocsDir)
	err = infra.RunServer(ctx, log, cfg.Addr, api.Handler(cfg.Token, cfg.UIToken))

	// Shutdown order matters, and defers alone cannot express it (they run
	// LIFO, so the deferred stop() would fire *after* the wait below).
	//
	//   1. stop()    cancel the context, telling the reaper to finish
	//   2. wg.Wait() let it return from its current tick
	//   3. deferred close() closes an idle pool, not a busy one
	//
	// Calling stop() here also covers the path where RunServer failed on its
	// own: the context would never be cancelled otherwise and wg.Wait()
	// would block forever.
	stop()
	wg.Wait()
	log.Info("shutdown complete")

	return err
}

// executablePath resolves the running binary for the admin console's node
// information, falling back to the invocation name.
func executablePath() string {
	path, err := os.Executable()
	if err != nil || path == "" {
		return os.Args[0]
	}
	return path
}

// openSQLite opens the embedded database and builds the sqlite repositories.
func openSQLite(ctx context.Context, cfg infra.Config, log *slog.Logger) (*storageDeps, error) {
	if err := os.MkdirAll(cfg.StorageDir, 0o750); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}
	db, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	closeOnce := &sync.Once{}
	return &storageDeps{
		tx:             sqlite.NewTxManager(db),
		taskRepo:       sqlite.NewTaskRepo(db),
		jobRepo:        sqlite.NewJobRepo(db),
		workerRepo:     sqlite.NewWorkerRepo(db),
		artifactRepo:   sqlite.NewArtifactRepo(db),
		uiReadRepo:     sqlite.NewUIReadRepo(db),
		adminReadRepo:  sqlite.NewAdminReadRepo(db),
		settingsRepo:   sqlite.NewWorkloadSettingsRepo(db),
		taskResultRepo: sqlite.NewTaskResultRepo(db),
		statsRepo:      sqlite.NewStatsRepo(db),
		ready:          func(ctx context.Context) error { return db.PingContext(ctx) },
		migrate:        func(ctx context.Context, log *slog.Logger) error { return sqlite.Migrate(ctx, db, log) },
		close:          func() { closeOnce.Do(func() { _ = db.Close() }) },
	}, nil
}

// openPostgres connects to PostgreSQL and builds the postgres repositories.
func openPostgres(ctx context.Context, cfg infra.Config, log *slog.Logger) (*storageDeps, error) {
	pool, err := infra.NewPool(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	closeOnce := &sync.Once{}
	return &storageDeps{
		tx:             postgres.NewTxManager(pool),
		taskRepo:       postgres.NewTaskRepo(pool),
		jobRepo:        postgres.NewJobRepo(pool),
		workerRepo:     postgres.NewWorkerRepo(pool),
		artifactRepo:   postgres.NewArtifactRepo(pool),
		uiReadRepo:     postgres.NewUIReadRepo(pool),
		adminReadRepo:  postgres.NewAdminReadRepo(pool),
		settingsRepo:   postgres.NewWorkloadSettingsRepo(pool),
		taskResultRepo: postgres.NewTaskResultRepo(pool),
		statsRepo:      postgres.NewStatsRepo(pool),
		ready:          func(ctx context.Context) error { return pool.Ping(ctx) },
		migrate:        func(ctx context.Context, log *slog.Logger) error { return postgres.Migrate(ctx, cfg.DatabaseURL, log) },
		close:          func() { closeOnce.Do(pool.Close) },
	}, nil
}
