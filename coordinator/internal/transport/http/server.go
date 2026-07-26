// Package http adapts the use-case layer to HTTP. Handlers decode requests,
// map them onto use-case inputs, and translate results and errors back — no
// business rules live here.
package http

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/metrics"
	tokenpkg "github.com/emil28092005/SciMesh/coordinator/internal/token"
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
	PreviewArtifact  *usecase.PreviewArtifact
}

type Server struct {
	uc                UseCases
	log               *slog.Logger
	requestTimeout    time.Duration
	heartbeatInterval time.Duration
	maxUploadBytes    int64
	// verifier validates userservice JWTs. nil disables user-JWT auth, leaving
	// only the shared service token — the pre-userservice behaviour.
	verifier *tokenpkg.Verifier
	// userserviceURL is the base URL the UI proxies login/registration to. Empty
	// keeps the static basic-auth UI.
	userserviceURL string
	// httpClient makes the login/register calls to the userservice.
	httpClient *http.Client
	// metrics holds the Prometheus registry and HTTP instrumentation.
	metrics *metrics.Metrics
	// ready probes downstream dependencies (the database) for /health. Kept as
	// a func so the transport layer never imports pgx.
	ready func(context.Context) error
}

func NewServer(uc UseCases, log *slog.Logger, requestTimeout, heartbeatInterval time.Duration,
	maxUploadBytes int64, jwtSecret, userserviceURL string, m *metrics.Metrics, ready func(context.Context) error) *Server {
	if m == nil {
		m = metrics.New()
	}
	return &Server{
		uc:                uc,
		log:               log,
		requestTimeout:    requestTimeout,
		heartbeatInterval: heartbeatInterval,
		maxUploadBytes:    maxUploadBytes,
		verifier:          tokenpkg.NewVerifier(jwtSecret),
		userserviceURL:    strings.TrimRight(userserviceURL, "/"),
		httpClient:        &http.Client{Timeout: 10 * time.Second},
		metrics:           m,
		ready:             ready,
	}
}

// uiSessionMode reports whether the operator UI authenticates via userservice
// login (cookie session) rather than the static basic-auth token. It needs both
// a verifier (to check the JWT locally) and a userservice URL (to issue it).
func (s *Server) uiSessionMode() bool {
	return s.verifier != nil && s.userserviceURL != ""
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
	// Unauthenticated like /health, so a Prometheus scraper needs no credential.
	mux.Handle("GET /metrics", s.metrics.Handler())

	hasBasicAuth := len(uiToken) > 0 && uiToken[0] != ""
	if s.uc.Dashboard != nil && (s.uiSessionMode() || hasBasicAuth) {
		ui := http.NewServeMux()

		// The operator application routes, all requiring an authenticated caller.
		app := []struct {
			pattern string
			handler http.HandlerFunc
		}{
			{"GET /ui", s.handleUIHome},
			{"GET /ui/jobs/new", s.handleUINewJob},
			{"GET /ui/jobs/{job_id}", s.handleUIJob},
			{"GET /ui/api/overview", s.handleUIOverviewJSON},
			{"GET /ui/api/jobs/{job_id}", s.handleUIJobJSON},
			{"POST /ui/api/jobs/{job_id}/cancel", s.handleCancelJob},
			{"POST /ui/api/jobs/upload", s.handleUploadDataset},
			{"GET /ui/jobs/{job_id}/artifacts/{artifact_id}", s.handleUIArtifactDownload},
			{"GET /ui/jobs/{job_id}/artifacts/{artifact_id}/preview", s.handleUIArtifactPreview},
		}

		if s.uiSessionMode() {
			// Public auth pages — reachable without a session so a user can log in.
			ui.HandleFunc("GET /ui/login", s.handleUILoginForm)
			ui.HandleFunc("POST /ui/login", s.handleUILogin)
			ui.HandleFunc("GET /ui/register", s.handleUIRegisterForm)
			ui.HandleFunc("POST /ui/register", s.handleUIRegister)
			ui.HandleFunc("POST /ui/logout", s.handleUILogout)
			gate := withUISession(s.verifier)
			for _, rt := range app {
				ui.Handle(rt.pattern, gate(rt.handler))
			}
			ui.Handle("GET /ui/profile", gate(http.HandlerFunc(s.handleUIProfile)))
			// Admin panel: session + admin role.
			ui.Handle("GET /ui/admin", chain(http.HandlerFunc(s.handleUIAdmin), gate, requireAdmin))
			ui.Handle("POST /ui/admin/user-action", chain(http.HandlerFunc(s.handleUIAdminUserAction), gate, requireAdmin))
		} else {
			for _, rt := range app {
				ui.HandleFunc(rt.pattern, rt.handler)
			}
		}

		common := []func(http.Handler) http.Handler{withRequestID, withAccessLog(s.log)}
		if !s.uiSessionMode() {
			common = append(common, withBasicAuth(uiToken[0]))
		}
		common = append(common, withSameOrigin)
		mux.Handle("/ui", chain(ui, common...))
		mux.Handle("/ui/", chain(ui, common...))
	} else {
		// More specific than the protected catch-all: UI absence is not an auth
		// failure and does not disclose that a UI feature is configured elsewhere.
		mux.HandleFunc("/ui", http.NotFound)
		mux.HandleFunc("/ui/", http.NotFound)
	}
	mux.Handle("/", chain(protected,
		withRequestID,        // outermost: every response gets an ID,
		withAccessLog(s.log), // including the 401s below
		withAuth(token, s.verifier),
	))
	// Measure every request once, outermost, with a normalized route label.
	return s.metrics.Middleware(mux)
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
