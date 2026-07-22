// Command coordinator is the SciMesh task-queue server. It owns all database
// access; workers reach it only over HTTP and never receive DB credentials.
// Migrations are a separate explicit command (see Makefile) — this binary never
// mutates schema at startup.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/emil28092005/SciMesh/coordinator/internal/infra"
	"github.com/emil28092005/SciMesh/coordinator/internal/storage/postgres"
	httptransport "github.com/emil28092005/SciMesh/coordinator/internal/transport/http"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// All work happens in run() so its defers (pool.Close, signal stop) still
	// execute: os.Exit skips deferred calls entirely.
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := infra.Load()
	if err != nil {
		return err
	}

	// One cancellation source for the whole process: HTTP server and reaper
	// both observe it and wind down together.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := infra.NewPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	// --- composition root: the only place that knows concrete types ---
	//
	// Wiring reads outward-in: adapters are constructed, then injected into
	// use cases through their ports. Nothing below this function can see a
	// pgxpool, and nothing above the repositories can see SQL.
	var (
		clk      = infra.NewClock()
		tx       = postgres.NewTxManager(pool)
		taskRepo = postgres.NewTaskRepo(pool)
		jobRepo  = postgres.NewJobRepo(pool)
	)

	useCases := httptransport.UseCases{
		CreateJob:    usecase.NewCreateJob(jobRepo, taskRepo, tx, clk),
		ClaimTask:    usecase.NewClaimTask(taskRepo, clk, cfg.LeaseDuration),
		RenewLease:   usecase.NewRenewLease(taskRepo, tx, clk, cfg.LeaseDuration),
		CompleteTask: usecase.NewCompleteTask(taskRepo, jobRepo, tx, clk),
		FailTask:     usecase.NewFailTask(taskRepo, jobRepo, tx, clk),
		GetJobStatus: usecase.NewGetJobStatus(jobRepo, taskRepo),
	}

	// Background workers are tracked so shutdown can wait for them. Without
	// this the process would exit while the reaper sat mid-UPDATE, and the
	// deferred pool.Close() would pull connections out from under it.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		infra.RunReaper(ctx, log, usecase.NewExpireLeases(taskRepo, clk), cfg.ReaperInterval)
	}()

	api := httptransport.NewServer(useCases, log, cfg.RequestTimeout)
	err = infra.RunServer(ctx, log, cfg.Addr, api.Handler(cfg.WorkerAuthToken))

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
