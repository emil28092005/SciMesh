package usecase

import (
	"context"

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
