package domain

import (
	"time"

	"github.com/google/uuid"
)

type WorkerStatus string

const (
	WorkerOnline  WorkerStatus = "online"
	WorkerBusy    WorkerStatus = "busy"
	WorkerOffline WorkerStatus = "offline"
)

// Worker is a registered process/machine allowed to claim tasks. Its
// capabilities are the allowlisted workload names it can run; the coordinator
// never hands it a task outside that set.
type Worker struct {
	ID              uuid.UUID
	Name            string
	Capabilities    []string
	Status          WorkerStatus
	LastHeartbeatAt time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewWorker registers a worker. A worker with no capabilities could never be
// handed a task, so an empty set is rejected rather than silently stored.
func NewWorker(name string, capabilities []string, now time.Time) (*Worker, error) {
	if len(capabilities) == 0 {
		return nil, ErrInvalidInput
	}
	return &Worker{
		ID:              uuid.New(),
		Name:            name,
		Capabilities:    capabilities,
		Status:          WorkerOnline,
		LastHeartbeatAt: now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}
