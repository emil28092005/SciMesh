// Package http adapts the use-case layer to HTTP. Handlers decode requests,
// map them onto use-case inputs, and translate results and errors back — no
// business rules live here.
package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// UseCases collects everything the transport needs. Depending on concrete
// use-case types (not one fat interface) keeps each handler's dependency
// explicit and the wiring visible in the composition root.
type UseCases struct {
	RegisterWorker   *usecase.RegisterWorker
	CreateJob        *usecase.CreateJob
	SubmitDataset    *usecase.SubmitDataset
	ClaimTask        *usecase.ClaimTask
	RenewLease       *usecase.RenewLease
	CompleteTask     *usecase.CompleteTask
	FailTask         *usecase.FailTask
	GetJobStatus     *usecase.GetJobStatus
	UploadArtifact   *usecase.UploadArtifact
	DownloadArtifact *usecase.DownloadArtifact
	GetTaskInput     *usecase.GetTaskInput
}

type Server struct {
	uc                UseCases
	log               *slog.Logger
	requestTimeout    time.Duration
	heartbeatInterval time.Duration
	maxUploadBytes    int64
	// ready probes downstream dependencies (the database) for /health. Kept as
	// a func so the transport layer never imports pgx.
	ready func(context.Context) error
}

func NewServer(uc UseCases, log *slog.Logger, requestTimeout, heartbeatInterval time.Duration,
	maxUploadBytes int64, ready func(context.Context) error) *Server {
	return &Server{
		uc:                uc,
		log:               log,
		requestTimeout:    requestTimeout,
		heartbeatInterval: heartbeatInterval,
		maxUploadBytes:    maxUploadBytes,
		ready:             ready,
	}
}

// Handler builds the router. Go 1.22's ServeMux matches on method and path
// wildcards, so no third-party router is needed.
func (s *Server) Handler(token string) http.Handler {
	protected := http.NewServeMux()
	protected.HandleFunc("POST /workers/register", s.handleRegister)
	protected.HandleFunc("POST /jobs", s.handleCreateJob)
	protected.HandleFunc("POST /jobs/upload", s.handleUploadDataset)
	protected.HandleFunc("GET /jobs/{job_id}", s.handleGetJob)
	protected.HandleFunc("POST /tasks/claim", s.handleClaim)
	protected.HandleFunc("GET /tasks/{task_id}/input", s.handleGetTaskInput)
	protected.HandleFunc("POST /tasks/{task_id}/heartbeat", s.handleHeartbeat)
	protected.HandleFunc("POST /tasks/{task_id}/result", s.handleResult)
	protected.HandleFunc("POST /tasks/{task_id}/failure", s.handleFailure)
	protected.HandleFunc("PUT /tasks/{task_id}/artifacts/{filename}", s.handleUploadArtifact)
	protected.HandleFunc("GET /artifacts/{artifact_id}/download", s.handleDownloadArtifact)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.Handle("/", chain(protected,
		withRequestID,        // outermost: every response gets an ID,
		withAccessLog(s.log), // including the 401s below
		withAuth(token),
	))
	return mux
}

// handleHealth reports readiness. It probes the database so an orchestrator
// learns the difference between "process is up" and "process can serve".
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.ready != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.ready(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
