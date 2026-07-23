package http

import (
	"embed"
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
	"time": func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
}).ParseFS(uiFiles, "templates/*.html"))

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
