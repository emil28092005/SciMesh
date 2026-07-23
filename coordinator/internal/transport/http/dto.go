package http

import (
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// Wire formats. Keeping them separate from domain entities means the API
// contract can evolve without reshaping the database, and nothing internal
// (version counters, other workers' errors) leaks by accident.

type createJobRequest struct {
	Workload   string         `json:"workload"`
	InputURI   string         `json:"input_uri"`
	Parameters map[string]any `json:"parameters"`
	Chunks     []chunkDTO     `json:"chunks"`
}

type chunkDTO struct {
	ChunkIndex  int            `json:"chunk_index"`
	Workload    string         `json:"workload"`
	InputURI    string         `json:"input_uri"`
	InputSHA256 string         `json:"input_sha256"`
	Parameters  map[string]any `json:"parameters"`
	MaxAttempts int            `json:"max_attempts"`
}

type registerRequest struct {
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
	// Accepted per the contract for forward compatibility; not yet persisted.
	CPUCount int `json:"cpu_count"`
	MemoryMB int `json:"memory_mb"`
}

type registerResponse struct {
	WorkerID                 uuid.UUID `json:"worker_id"`
	HeartbeatIntervalSeconds int       `json:"heartbeat_interval_seconds"`
}

type claimRequest struct {
	WorkerID     string   `json:"worker_id"`
	Capabilities []string `json:"capabilities"`
	// Accepted per the contract; the coordinator leases one task per call.
	MaxConcurrency int `json:"max_concurrency"`
}

type heartbeatRequest struct {
	WorkerID string `json:"worker_id"`
	Attempt  int    `json:"attempt"`
}

type resultRequest struct {
	WorkerID string         `json:"worker_id"`
	Attempt  int            `json:"attempt"`
	Result   resultManifest `json:"result"`
	Metrics  map[string]any `json:"metrics"`
}

// resultManifest references the artifact the worker already uploaded. sha256 and
// content_type are accepted for the worker's own cross-checking; the coordinator
// trusts its own stored metadata, not these.
type resultManifest struct {
	ArtifactID  uuid.UUID `json:"artifact_id"`
	SHA256      string    `json:"sha256"`
	ContentType string    `json:"content_type"`
}

type failureRequest struct {
	WorkerID     string `json:"worker_id"`
	Attempt      int    `json:"attempt"`
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
	Retryable    bool   `json:"retryable"`
}

type jobResponse struct {
	ID     uuid.UUID `json:"id"`
	Status string    `json:"status"`
}

type taskResponse struct {
	ID     uuid.UUID `json:"id"`
	JobID  uuid.UUID `json:"job_id"`
	Status string    `json:"status"`
}

type inputRef struct {
	URI    string `json:"uri"`
	SHA256 string `json:"sha256"`
}

type claimedTaskResponse struct {
	TaskID         uuid.UUID      `json:"task_id"`
	JobID          uuid.UUID      `json:"job_id"`
	ChunkIndex     int            `json:"chunk_index"`
	Workload       string         `json:"workload"`
	Input          inputRef       `json:"input"`
	Parameters     map[string]any `json:"parameters"`
	Attempt        int            `json:"attempt"`
	LeaseExpiresAt time.Time      `json:"lease_expires_at"`
}

type uploadJobResponse struct {
	JobID           uuid.UUID `json:"job_id"`
	TaskCount       int       `json:"task_count"`
	InputArtifactID uuid.UUID `json:"input_artifact_id"`
}

type jobProgressResponse struct {
	ID      uuid.UUID `json:"id"`
	Status  string    `json:"status"`
	Total   int       `json:"total"`
	Pending int       `json:"pending"`
	Leased  int       `json:"leased"`
	Done    int       `json:"completed"`
	Failed  int       `json:"failed"`
}

type uploadArtifactResponse struct {
	ArtifactID uuid.UUID `json:"artifact_id"`
	URI        string    `json:"uri"`
	SHA256     string    `json:"sha256"`
	SizeBytes  int64     `json:"size_bytes"`
}

type errorResponse struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

func toClaimedTaskResponse(c domain.ClaimedTask) claimedTaskResponse {
	// A shard's input lives in the coordinator; hand the worker a URL to fetch
	// it from. A URI-based task keeps its external URI.
	uri := c.InputURI
	if c.InputArtifactID != nil {
		uri = "/tasks/" + c.TaskID.String() + "/input"
	}
	return claimedTaskResponse{
		TaskID:         c.TaskID,
		JobID:          c.JobID,
		ChunkIndex:     c.ChunkIndex,
		Workload:       c.Workload,
		Input:          inputRef{URI: uri, SHA256: c.InputSHA256},
		Parameters:     c.Parameters,
		Attempt:        c.Attempt,
		LeaseExpiresAt: c.LeaseExpiresAt,
	}
}

func toJobProgressResponse(p domain.JobProgress) jobProgressResponse {
	return jobProgressResponse{
		ID:      p.Job.ID,
		Status:  string(p.DeriveStatus()),
		Total:   p.Total,
		Pending: p.Pending,
		Leased:  p.Leased,
		Done:    p.Done,
		Failed:  p.Failed,
	}
}
