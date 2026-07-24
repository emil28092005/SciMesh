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
	ReduceJob        *usecase.ReduceJob
	FailTask         *usecase.FailTask
	GetJobStatus     *usecase.GetJobStatus
	GetJobResult     *usecase.GetJobResult
	CancelJob        *usecase.CancelJob
	UploadArtifact   *usecase.UploadArtifact
	DownloadArtifact *usecase.DownloadArtifact
	GetTaskInput     *usecase.GetTaskInput
	Dashboard        *usecase.Dashboard
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
func (s *Server) Handler(token string, uiToken ...string) http.Handler {
	protected := http.NewServeMux()
	protected.HandleFunc("POST /workers/register", s.handleRegister)
	protected.HandleFunc("POST /jobs", s.handleCreateJob)
	protected.HandleFunc("POST /jobs/upload", s.handleUploadDataset)
	protected.HandleFunc("GET /jobs/{job_id}", s.handleGetJob)
	protected.HandleFunc("GET /jobs/{job_id}/result", s.handleGetJobResult)
	protected.HandleFunc("POST /jobs/{job_id}/cancel", s.handleCancelJob)
	protected.HandleFunc("POST /tasks/claim", s.handleClaim)
	protected.HandleFunc("GET /tasks/{task_id}/input", s.handleGetTaskInput)
	protected.HandleFunc("POST /tasks/{task_id}/heartbeat", s.handleHeartbeat)
	protected.HandleFunc("POST /tasks/{task_id}/result", s.handleResult)
	protected.HandleFunc("POST /tasks/{task_id}/failure", s.handleFailure)
	protected.HandleFunc("PUT /tasks/{task_id}/artifacts/{filename}", s.handleUploadArtifact)
	protected.HandleFunc("GET /artifacts/{artifact_id}/download", s.handleDownloadArtifact)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	if len(uiToken) > 0 && uiToken[0] != "" && s.uc.Dashboard != nil {
		ui := http.NewServeMux()
		ui.HandleFunc("GET /ui", s.handleUIHome)
		ui.HandleFunc("GET /ui/jobs/new", s.handleUINewJob)
		ui.HandleFunc("GET /ui/jobs/{job_id}", s.handleUIJob)
		ui.HandleFunc("GET /ui/api/overview", s.handleUIOverviewJSON)
		ui.HandleFunc("GET /ui/api/jobs/{job_id}", s.handleUIJobJSON)
		ui.HandleFunc("POST /ui/api/jobs/{job_id}/cancel", s.handleCancelJob)
		ui.HandleFunc("POST /ui/api/jobs/upload", s.handleUploadDataset)
		ui.HandleFunc("GET /ui/jobs/{job_id}/artifacts/{artifact_id}", s.handleUIArtifactDownload)
		mux.Handle("/ui", chain(ui, withRequestID, withAccessLog(s.log), withBasicAuth(uiToken[0]), withSameOrigin))
		mux.Handle("/ui/", chain(ui, withRequestID, withAccessLog(s.log), withBasicAuth(uiToken[0]), withSameOrigin))
	} else {
		// More specific than the protected catch-all: UI absence is not an auth
		// failure and does not disclose that a UI feature is configured elsewhere.
		mux.HandleFunc("/ui", http.NotFound)
		mux.HandleFunc("/ui/", http.NotFound)
	}
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
