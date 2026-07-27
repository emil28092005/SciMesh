package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/authctx"
	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// authorizeWorkerOwner binds a JWT-authenticated requester to a worker. The
// shared coordinator token intentionally has no requester and retains its
// existing operator privileges.
func authorizeWorkerOwner(ctx context.Context, workers WorkerRepository, workerID string) error {
	requester, ok := authctx.From(ctx)
	if !ok {
		return nil
	}
	id, err := uuid.Parse(workerID)
	if err != nil {
		return domain.ErrWorkerNotFound
	}
	worker, err := workers.Get(ctx, id)
	if err != nil {
		return err
	}
	if worker.OwnerID == nil || *worker.OwnerID != requester.UserID {
		// Mask ownership and existence from another user.
		return domain.ErrWorkerNotFound
	}
	return nil
}
