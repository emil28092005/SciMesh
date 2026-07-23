package http

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

//go:embed templates/*.html
var uiFiles embed.FS

var uiTemplates = template.Must(template.New("ui").Funcs(template.FuncMap{
	"time":              formatUITime,
	"statusLabel":       uiStatusLabel,
	"statusHint":        uiStatusHint,
	"statusClass":       uiStatusClass,
	"taskErrorLabel":    uiTaskErrorLabel,
	"taskErrorHint":     uiTaskErrorHint,
	"workerStatusLabel": uiWorkerStatusLabel,
	"workloadLabel":     uiWorkloadLabel,
	"progressPercent":   uiProgressPercent,
	"cancellable":       uiCancellable,
	"bytes":             uiBytes,
	"add":               func(a, b int) int { return a + b },
}).ParseFS(uiFiles, "templates/*.html"))

func formatUITime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("02.01.2006 15:04 UTC")
}

func uiStatusLabel(status string) string {
	switch status {
	case "pending":
		return "Waiting for a worker"
	case "leased":
		return "Assigned to a worker"
	case "running":
		return "Running"
	case "completed":
		return "Tasks complete"
	case "failed":
		return "Needs attention"
	case "cancelled":
		return "Stopped"
	default:
		return status
	}
}

func uiStatusHint(status string) string {
	switch status {
	case "pending":
		return "Waiting for an available worker with the required capability."
	case "leased":
		return "A worker has claimed the task and should begin processing shortly."
	case "running":
		return "A worker is reading a shard, calculating fingerprints, and uploading its result through the coordinator."
	case "completed":
		return "Every shard task is complete. Files below are still partial results."
	case "failed":
		return "One or more shard tasks failed. Open the task list below for details."
	case "cancelled":
		return "The operator stopped this job. No new shards can be claimed."
	default:
		return "Status reported by the coordinator."
	}
}

func uiStatusClass(status string) string {
	switch status {
	case "completed":
		return "success"
	case "failed":
		return "danger"
	case "cancelled":
		return "waiting"
	case "running", "leased":
		return "active"
	default:
		return "waiting"
	}
}

func uiWorkerStatusLabel(status string) string {
	switch status {
	case "online":
		return "Available"
	case "offline":
		return "Offline"
	default:
		return status
	}
}

// uiTaskErrorLabel deliberately maps worker implementation errors to an
// operator-facing diagnosis. Raw subprocess commands and local paths belong in
// the worker terminal, not in the web UI.
func uiTaskErrorLabel(errorCode string) string {
	switch errorCode {
	case "CalledProcessError":
		return "Local calculation failed"
	case "ValueError":
		return "Task input could not be processed"
	case "CoordinatorTransientError":
		return "Coordinator connection was interrupted"
	case "CoordinatorConflictError":
		return "Worker lease was no longer valid"
	case "FileNotFoundError":
		return "Local task file is missing"
	default:
		return errorCode
	}
}

func uiTaskErrorHint(errorCode string) string {
	switch errorCode {
	case "CalledProcessError":
		return "The local SciMesh command stopped before it could upload a result. Check the worker terminal for the original error."
	case "ValueError":
		return "The coordinator task or its downloaded input did not meet the worker validation rules."
	case "CoordinatorTransientError":
		return "The worker will retry after the coordinator connection is available again."
	case "CoordinatorConflictError":
		return "Another worker or a lease timeout changed this task before completion."
	case "FileNotFoundError":
		return "The worker could not find one of its local task files. Restart it with an absolute --work-dir."
	default:
		return "Check the worker terminal for the original error details."
	}
}

func uiWorkloadLabel(workload string) string {
	switch workload {
	case "similarity-search", "similarity_search":
		return "Molecule similarity search"
	case "similarity-graph", "similarity_graph":
		return "Molecular similarity graph"
	default:
		return workload
	}
}

func uiCancellable(status string) bool {
	return status == "pending" || status == "running"
}

func uiProgressPercent(completed, failed, cancelled, total int) int {
	if total <= 0 {
		return 0
	}
	percent := (completed + failed + cancelled) * 100 / total
	if percent > 100 {
		return 100
	}
	return percent
}

func uiBytes(n int64) string {
	const kib = 1024
	if n < kib {
		return fmt.Sprintf("%d B", n)
	}
	if n < kib*kib {
		return fmt.Sprintf("%.1f KiB", float64(n)/kib)
	}
	if n < kib*kib*kib {
		return fmt.Sprintf("%.1f MiB", float64(n)/(kib*kib))
	}
	return fmt.Sprintf("%.1f GiB", float64(n)/(kib*kib*kib))
}

func (s *Server) renderUI(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
	if err := uiTemplates.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("render UI", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) handleUIHome(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	view, err := s.uc.Dashboard.Overview(ctx, 20)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.renderUI(w, "dashboard.html", view)
}

func (s *Server) handleUINewJob(w http.ResponseWriter, r *http.Request) {
	s.renderUI(w, "new-job.html", nil)
}

func (s *Server) uiJobID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	return s.pathUUID(w, r, "job_id")
}

func (s *Server) handleUIJob(w http.ResponseWriter, r *http.Request) {
	jobID, ok := s.uiJobID(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	view, err := s.uc.Dashboard.JobDetail(ctx, jobID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.renderUI(w, "job.html", view)
}

func (s *Server) handleUIJobJSON(w http.ResponseWriter, r *http.Request) {
	jobID, ok := s.uiJobID(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	view, err := s.uc.Dashboard.JobDetail(ctx, jobID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleUIArtifactDownload(w http.ResponseWriter, r *http.Request) {
	jobID, ok := s.uiJobID(w, r)
	if !ok {
		return
	}
	artifactID, err := uuid.Parse(r.PathValue("artifact_id"))
	if err != nil {
		s.writeError(w, r, domain.ErrInvalidInput)
		return
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	belongs, err := s.uc.Dashboard.ArtifactBelongsToJob(ctx, jobID, artifactID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !belongs {
		s.writeError(w, r, domain.ErrArtifactNotFound)
		return
	}
	// Reuse the coordinator-owned blob stream after the job-scoped check above.
	art, body, err := s.uc.DownloadArtifact.Execute(ctx, artifactID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer func() {
		if err := body.Close(); err != nil {
			s.log.Warn("close downloaded UI artifact", "artifact_id", artifactID, "err", err)
		}
	}()
	w.Header().Set("Content-Type", art.ContentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": art.Filename}))
	w.Header().Set("Content-Length", strconv.FormatInt(art.SizeBytes, 10))
	w.Header().Set("X-Checksum-SHA256", art.SHA256)
	_, _ = io.Copy(w, body)
}
