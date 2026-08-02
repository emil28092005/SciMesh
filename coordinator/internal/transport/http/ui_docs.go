package http

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// handleUIDocsIndex redirects /ui/docs to the trailing-slash form so the
// wildcard route below can resolve index.html.
func (s *Server) handleUIDocsIndex(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/docs/", http.StatusPermanentRedirect)
}

// handleUIDocs serves the built MkDocs site (site/) as static files. The
// configured docs directory is an operator-supplied path, never derived from
// a request; path traversal is rejected by joining against the cleaned root
// and checking the result stays inside it.
func (s *Server) handleUIDocs(w http.ResponseWriter, r *http.Request) {
	if s.docsDir == "" {
		s.renderUI(w, "docs-unavailable.html", nil)
		return
	}
	root, err := filepath.Abs(s.docsDir)
	if err != nil {
		s.renderUI(w, "docs-unavailable.html", nil)
		return
	}
	clean := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/ui/docs/"))
	target, err := filepath.Abs(filepath.Join(root, clean))
	if err != nil {
		s.renderUI(w, "docs-unavailable.html", nil)
		return
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		if err == nil && info.IsDir() {
			target = filepath.Join(target, "index.html")
			info, err = os.Stat(target)
		}
		if err != nil || info.IsDir() {
			s.renderUI(w, "docs-unavailable.html", nil)
			return
		}
	}
	http.ServeFile(w, r, target)
}
