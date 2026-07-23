package usecase

import (
	"context"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// RegisterWorker records a worker in the registry and hands back its identity.
type RegisterWorker struct {
	workers WorkerRepository
	clk     Clock
}

func NewRegisterWorker(workers WorkerRepository, clk Clock) *RegisterWorker {
	return &RegisterWorker{workers: workers, clk: clk}
}

func (uc *RegisterWorker) Execute(ctx context.Context, in RegisterWorkerInput) (*domain.Worker, error) {
	w, err := domain.NewWorker(in.Name, in.Capabilities, uc.clk.Now())
	if err != nil {
		return nil, err
	}
	if err := uc.workers.Insert(ctx, w); err != nil {
		return nil, err
	}
	return w, nil
}

// MarkWorkersOffline is the liveness reaper: workers that stopped heartbeating
// longer ago than `after` are flipped to offline.
type MarkWorkersOffline struct {
	workers WorkerRepository
	clk     Clock
	after   time.Duration
}

func NewMarkWorkersOffline(workers WorkerRepository, clk Clock, after time.Duration) *MarkWorkersOffline {
	return &MarkWorkersOffline{workers: workers, clk: clk, after: after}
}

func (uc *MarkWorkersOffline) Execute(ctx context.Context) (int64, error) {
	return uc.workers.MarkStaleOffline(ctx, uc.clk.Now().Add(-uc.after))
}
