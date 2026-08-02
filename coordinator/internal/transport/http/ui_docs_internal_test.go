package http

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func docsTestServer(t *testing.T, docsDir string) *Server {
	t.Helper()
	return &Server{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		docsDir: docsDir,
	}
}

func TestUIDocsServesIndexAndNestedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<h1>Home</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "api")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "page.html"), []byte("<h1>API page</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := docsTestServer(t, root)

	index := httptest.NewRecorder()
	server.handleUIDocs(index, httptest.NewRequest(http.MethodGet, "/ui/docs/", nil))
	if index.Code != http.StatusOK || !strings.Contains(index.Body.String(), "<h1>Home</h1>") {
		t.Fatalf("index = %d %q", index.Code, index.Body.String())
	}

	page := httptest.NewRecorder()
	server.handleUIDocs(page, httptest.NewRequest(http.MethodGet, "/ui/docs/api/page.html", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "<h1>API page</h1>") {
		t.Fatalf("nested page = %d %q", page.Code, page.Body.String())
	}
}

func TestUIDocsRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := docsTestServer(t, root)

	request := httptest.NewRequest(http.MethodGet, "/ui/docs/../secret.txt", nil)
	request.URL.Path = "/ui/docs/../secret.txt"
	recorder := httptest.NewRecorder()
	server.handleUIDocs(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("traversal status = %d, want 404", recorder.Code)
	}
}

func TestUIDocsShowsBuildHintWhenDisabledOrMissing(t *testing.T) {
	disabled := docsTestServer(t, "")
	recorder := httptest.NewRecorder()
	disabled.handleUIDocs(recorder, httptest.NewRequest(http.MethodGet, "/ui/docs/", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Documentation is not available") {
		t.Fatalf("disabled docs = %d %q", recorder.Code, recorder.Body.String())
	}

	missing := docsTestServer(t, filepath.Join(t.TempDir(), "does-not-exist"))
	recorder = httptest.NewRecorder()
	missing.handleUIDocs(recorder, httptest.NewRequest(http.MethodGet, "/ui/docs/", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Documentation is not available") {
		t.Fatalf("missing docs = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestUIDocsIndexRedirectsToTrailingSlash(t *testing.T) {
	server := docsTestServer(t, t.TempDir())
	recorder := httptest.NewRecorder()
	server.handleUIDocsIndex(recorder, httptest.NewRequest(http.MethodGet, "/ui/docs", nil))
	if recorder.Code != http.StatusPermanentRedirect || recorder.Header().Get("Location") != "/ui/docs/" {
		t.Fatalf("redirect = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
}
