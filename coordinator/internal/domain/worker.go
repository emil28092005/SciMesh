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

// WorkerTrust says whether a worker's results are accepted directly or must
// clear quorum cross-checking.
type WorkerTrust string

const (
	// WorkerTrusted — lab machine (shared token) or a verified/admin contributor.
	WorkerTrusted WorkerTrust = "trusted"
	// WorkerUntrusted — a plain enthusiast; results are quarantined until quorum.
	WorkerUntrusted WorkerTrust = "untrusted"
)

// Worker is a registered process/machine allowed to claim tasks. Its
// capabilities are the allowlisted workload names it can run; the coordinator
// never hands it a task outside that set.
type Worker struct {
	ID           uuid.UUID
	Name         string
	Capabilities []string
	Status       WorkerStatus
	// OwnerID is the userservice user who registered this worker; nil for a
	// worker registered with the shared service token.
	OwnerID *uuid.UUID
	// TrustLevel decides whether this worker's results need quorum.
	TrustLevel      WorkerTrust
	LastHeartbeatAt time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewWorker registers a worker. A worker with no capabilities could never be
// handed a task, so an empty set is rejected rather than silently stored.
//
// Trust defaults to WorkerTrusted (the shared-token lab worker); the caller
// overrides it for a volunteer registered through the userservice.
func NewWorker(name string, capabilities []string, now time.Time) (*Worker, error) {
	if len(capabilities) == 0 {
		return nil, ErrInvalidInput
	}
	return &Worker{
		ID:              uuid.New(),
		Name:            name,
		Capabilities:    capabilities,
		Status:          WorkerOnline,
		TrustLevel:      WorkerTrusted,
		LastHeartbeatAt: now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}
