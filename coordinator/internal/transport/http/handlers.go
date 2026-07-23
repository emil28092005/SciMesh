package http

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// Every handler follows the same shape: decode, map to a use-case input,
// execute, translate. Anything resembling a rule belongs one layer inward.

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()

	var req createJobRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}

	in := usecase.CreateJobInput{
		Workload:   req.Workload,
		InputURI:   req.InputURI,
		Parameters: req.Parameters,
	}
	for _, c := range req.Chunks {
		in.Chunks = append(in.Chunks, usecase.ChunkInput(c))
	}

	job, err := s.uc.CreateJob.Execute(ctx, in)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, jobResponse{ID: job.ID, Status: string(job.Status)})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()

	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}

	worker, err := s.uc.RegisterWorker.Execute(ctx, usecase.RegisterWorkerInput{
		Name:         req.Name,
		Capabilities: req.Capabilities,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, registerResponse{
		WorkerID:                 worker.ID,
		HeartbeatIntervalSeconds: int(s.heartbeatInterval.Seconds()),
	})
}

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()

	var req claimRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}

	claimed, err := s.uc.ClaimTask.Execute(ctx, usecase.ClaimTaskInput{
		WorkerID:  req.WorkerID,
		Workloads: req.Capabilities,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if claimed == nil {
		w.WriteHeader(http.StatusNoContent) // empty queue, not an error
		return
	}
	writeJSON(w, http.StatusOK, toClaimedTaskResponse(*claimed))
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()

	taskID, ok := s.pathUUID(w, r, "task_id")
	if !ok {
		return
	}
	var req heartbeatRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}

	claimed, err := s.uc.RenewLease.Execute(ctx, usecase.RenewLeaseInput{
		TaskID:   taskID,
		WorkerID: req.WorkerID,
		Attempt:  req.Attempt,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toClaimedTaskResponse(*claimed))
}

func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()

	taskID, ok := s.pathUUID(w, r, "task_id")
	if !ok {
		return
	}
	var req resultRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}

	task, err := s.uc.CompleteTask.Execute(ctx, usecase.CompleteTaskInput{
		TaskID:           taskID,
		WorkerID:         req.WorkerID,
		Attempt:          req.Attempt,
		ResultArtifactID: req.Result.ArtifactID,
		Metrics:          req.Metrics,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, taskResponse{ID: task.ID, JobID: task.JobID, Status: string(task.Status)})
}

func (s *Server) handleFailure(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()

	taskID, ok := s.pathUUID(w, r, "task_id")
	if !ok {
		return
	}
	var req failureRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}

	task, err := s.uc.FailTask.Execute(ctx, usecase.FailTaskInput{
		TaskID:       taskID,
		WorkerID:     req.WorkerID,
		Attempt:      req.Attempt,
		ErrorCode:    req.ErrorCode,
		ErrorMessage: req.ErrorMessage,
		Retryable:    req.Retryable,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, taskResponse{ID: task.ID, JobID: task.JobID, Status: string(task.Status)})
}

// handleUploadArtifact streams a worker's partial result into blob storage. It
// deliberately does not use the short request timeout — a large shard upload
// would trip it — and reads identity from headers per the contract (§5.5).
func (s *Server) handleUploadArtifact(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.pathUUID(w, r, "task_id")
	if !ok {
		return
	}
	attempt, err := strconv.Atoi(r.Header.Get("X-Task-Attempt"))
	if err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}

	art, err := s.uc.UploadArtifact.Execute(r.Context(), usecase.UploadArtifactInput{
		TaskID:      taskID,
		WorkerID:    r.Header.Get("X-Worker-ID"),
		Attempt:     attempt,
		Filename:    r.PathValue("filename"),
		ContentType: r.Header.Get("Content-Type"),
		Body:        r.Body,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, uploadArtifactResponse{
		ArtifactID: art.ID,
		URI:        "/artifacts/" + art.ID.String() + "/download",
		SHA256:     art.SHA256,
		SizeBytes:  art.SizeBytes,
	})
}

// handleDownloadArtifact streams an artifact's bytes back to the caller.
func (s *Server) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	artifactID, ok := s.pathUUID(w, r, "artifact_id")
	if !ok {
		return
	}
	art, body, err := s.uc.DownloadArtifact.Execute(r.Context(), artifactID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", art.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(art.SizeBytes, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", art.Filename))
	w.Header().Set("X-Checksum-SHA256", art.SHA256)
	_, _ = io.Copy(w, body)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()

	jobID, ok := s.pathUUID(w, r, "job_id")
	if !ok {
		return
	}
	progress, err := s.uc.GetJobStatus.Execute(ctx, jobID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toJobProgressResponse(progress))
}

// --- helpers ---

func (s *Server) reqCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), s.requestTimeout)
}

func (s *Server) pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return uuid.Nil, false
	}
	return id, true
}
