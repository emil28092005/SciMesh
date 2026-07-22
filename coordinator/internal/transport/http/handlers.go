package http

import (
	"context"
	"net/http"

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
		Workloads: req.Workloads,
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
		TaskID:       taskID,
		WorkerID:     req.WorkerID,
		Attempt:      req.Attempt,
		ResultURI:    req.ResultURI,
		ResultSHA256: req.ResultSHA256,
		Metrics:      req.Metrics,
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
