package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/emil28092005/SciMesh/coordinator/internal/infra"
	"github.com/emil28092005/SciMesh/coordinator/internal/storage/blob"
	"github.com/emil28092005/SciMesh/coordinator/internal/storage/postgres"
	httptransport "github.com/emil28092005/SciMesh/coordinator/internal/transport/http"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

func main() {
	// All work happens in run() so its defers (pool.Close, log flush, signal
	// stop) still execute: os.Exit skips deferred calls entirely.
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	// Bootstrap logger, used only until config says where logs should go. It
	// writes to stderr so it never contaminates the configured stdout stream.
	boot := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := infra.LoadConfig()
	if err != nil {
		boot.Error("load config", "err", err)
		return err
	}

	// The real logger: stdout plus an optional rotated file (LOG_FILE).
	log, logCloser, err := infra.NewLogger(cfg)
	if err != nil {
		boot.Error("init logger", "err", err)
		return err
	}
	defer func() { _ = logCloser.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := infra.NewPool(ctx, cfg, log)
	if err != nil {
		log.Error("connect database", "err", err)
		return err
	}
	defer pool.Close()

	blobStore, err := blob.NewFSStore(cfg.StorageDir)
	if err != nil {
		log.Error("init blob storage", "err", err)
		return err
	}

	var (
		clk          = infra.NewClock()
		tx           = postgres.NewTxManager(pool)
		taskRepo     = postgres.NewTaskRepo(pool)
		jobRepo      = postgres.NewJobRepo(pool)
		workerRepo   = postgres.NewWorkerRepo(pool)
		artifactRepo = postgres.NewArtifactRepo(pool)
		uiReadRepo   = postgres.NewUIReadRepo(pool)
	)

	useCases := httptransport.UseCases{
		RegisterWorker:   usecase.NewRegisterWorker(workerRepo, clk),
		CreateJob:        usecase.NewCreateJob(jobRepo, taskRepo, tx, clk),
		SubmitDataset:    usecase.NewSubmitDataset(blobStore, artifactRepo, jobRepo, taskRepo, tx, clk, cfg.DefaultMaxAttempts),
		ClaimTask:        usecase.NewClaimTask(taskRepo, jobRepo, workerRepo, tx, clk, cfg.LeaseDuration),
		RenewLease:       usecase.NewRenewLease(taskRepo, workerRepo, tx, clk, cfg.LeaseDuration),
		CompleteTask:     usecase.NewCompleteTask(taskRepo, jobRepo, artifactRepo, tx, clk),
		ReduceJob:        usecase.NewReduceJob(jobRepo, taskRepo, artifactRepo, blobStore, tx, clk),
		FailTask:         usecase.NewFailTask(taskRepo, jobRepo, tx, clk),
		GetJobStatus:     usecase.NewGetJobStatus(jobRepo, taskRepo),
		CancelJob:        usecase.NewCancelJob(jobRepo, taskRepo, tx, clk),
		UploadArtifact:   usecase.NewUploadArtifact(taskRepo, artifactRepo, blobStore, tx, clk),
		DownloadArtifact: usecase.NewDownloadArtifact(artifactRepo, blobStore),
		GetJobResult:     usecase.NewGetJobResult(jobRepo, usecase.NewDownloadArtifact(artifactRepo, blobStore)),
		GetTaskInput:     usecase.NewGetTaskInput(taskRepo, artifactRepo, blobStore),
		Dashboard:        usecase.NewDashboard(uiReadRepo),
	}

	// Background reapers are tracked so shutdown can wait for them. Without this
	// the process would exit mid-UPDATE, and the deferred pool.Close() would pull
	// connections out from under them.
	expireLeases := usecase.NewExpireLeases(taskRepo, jobRepo, tx, clk)
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

	// pool.Ping backs /health: readiness means the database answers, not just
	// that the process is alive.
	api := httptransport.NewServer(useCases, log, cfg.RequestTimeout, cfg.HeartbeatInterval, cfg.MaxUploadBytes, pool.Ping)
	err = infra.RunServer(ctx, log, cfg.Addr, api.Handler(cfg.Token, cfg.UIToken))

	// Shutdown order matters, and defers alone cannot express it (they run
	// LIFO, so the deferred stop() would fire *after* the wait below).
	//
	//   1. stop()    cancel the context, telling the reaper to finish
	//   2. wg.Wait() let it return from its current tick
	//   3. deferred pool.Close() closes an idle pool, not a busy one
	//
	// Calling stop() here also covers the path where RunServer failed on its
	// own: the context would never be cancelled otherwise and wg.Wait()
	// would block forever.
	stop()
	wg.Wait()
	log.Info("shutdown complete")

	return err
}
