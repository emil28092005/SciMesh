package usecase

import (
	"io"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// Use-case boundary types. Adapters map their wire formats onto these, so the
// HTTP shape can change without touching business code.

type CreateJobInput struct {
	Workload   string
	InputURI   string
	Parameters map[string]any
	Chunks     []ChunkInput
}

type ChunkInput struct {
	ChunkIndex  int
	Workload    string
	InputURI    string
	InputSHA256 string
	Parameters  map[string]any
	MaxAttempts int
}

type RegisterWorkerInput struct {
	Name         string
	Capabilities []string
	// OwnerID is the userservice user registering this worker; nil for a
	// shared-token registration. TrustLevel is resolved by the transport layer
	// from how the caller authenticated.
	OwnerID    *uuid.UUID
	TrustLevel domain.WorkerTrust
}

type ClaimTaskInput struct {
	WorkerID  string
	Workloads []string
}

type RenewLeaseInput struct {
	TaskID   uuid.UUID
	WorkerID string
	Attempt  int
}

type CompleteTaskInput struct {
	TaskID           uuid.UUID
	WorkerID         string
	Attempt          int
	ResultArtifactID uuid.UUID
	Metrics          map[string]any
}

type SubmitDatasetInput struct {
	Workload     string
	Parameters   map[string]any
	RowsPerShard int
	// MaxRows limits how many data rows are turned into shards. Zero means the
	// whole uploaded dataset; the input artifact itself remains stored intact.
	MaxRows     int
	Filename    string
	ContentType string
	Body        io.Reader
}

type SubmitDatasetResult struct {
	JobID           uuid.UUID
	TaskCount       int
	InputArtifactID uuid.UUID
}

type UploadArtifactInput struct {
	TaskID      uuid.UUID
	WorkerID    string
	Attempt     int
	Filename    string
	ContentType string
	Body        io.Reader
}

type FailTaskInput struct {
	TaskID       uuid.UUID
	WorkerID     string
	Attempt      int
	ErrorCode    string
	ErrorMessage string
	Retryable    bool
}
