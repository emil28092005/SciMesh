// Package http adapts the use-case layer to HTTP. Handlers decode requests,
// map them onto use-case inputs, and translate results and errors back — no
// business rules live here.
package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// UseCases collects everything the transport needs. Depending on concrete
// use-case types (not one fat interface) keeps each handler's dependency
// explicit and the wiring visible in the composition root.
type UseCases struct {
	CreateJob    *usecase.CreateJob
	ClaimTask    *usecase.ClaimTask
	RenewLease   *usecase.RenewLease
	CompleteTask *usecase.CompleteTask
	FailTask     *usecase.FailTask
	GetJobStatus *usecase.GetJobStatus
}

type Server struct {
	uc             UseCases
	log            *slog.Logger
	requestTimeout time.Duration
}

func NewServer(uc UseCases, log *slog.Logger, requestTimeout time.Duration) *Server {
	return &Server{uc: uc, log: log, requestTimeout: requestTimeout}
}

// Handler builds the router. Go 1.22's ServeMux matches on method and path
// wildcards, so no third-party router is needed.
func (s *Server) Handler(token string) http.Handler {
	protected := http.NewServeMux()
	protected.HandleFunc("POST /jobs", s.handleCreateJob)
	protected.HandleFunc("GET /jobs/{job_id}", s.handleGetJob)
	protected.HandleFunc("POST /tasks/claim", s.handleClaim)
	protected.HandleFunc("POST /tasks/{task_id}/heartbeat", s.handleHeartbeat)
	protected.HandleFunc("POST /tasks/{task_id}/result", s.handleResult)
	protected.HandleFunc("POST /tasks/{task_id}/failure", s.handleFailure)

	mux := http.NewServeMux()
	// A more specific pattern wins, so /health stays outside the auth wall.
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.Handle("/", chain(protected,
		withRequestID,        // outermost: every response gets an ID,
		withAccessLog(s.log), // including the 401s below
		withAuth(token),
	))
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
