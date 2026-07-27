package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeRoute(t *testing.T) {
	cases := map[string]string{
		"/health": "/health",
		"/jobs/3f2504e0-4f89-41d3-9a0c-0305e82c3301":         "/jobs/{id}",
		"/tasks/3f2504e0-4f89-41d3-9a0c-0305e82c3301/result": "/tasks/{id}/result",
		"/ui/jobs/12345": "/ui/jobs/{id}",
		"/":              "/",
	}
	for in, want := range cases {
		if got := normalizeRoute(in); got != want {
			t.Errorf("normalizeRoute(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMiddlewareAndHandler(t *testing.T) {
	m := New()
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/jobs/3f2504e0-4f89-41d3-9a0c-0305e82c3301", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	// Scrape and confirm the request was recorded under the normalized route.
	rec := httptest.NewRecorder()
	greq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, greq)

	body := rec.Body.String()
	if !strings.Contains(body, `scimesh_http_requests_total{method="POST",route="/jobs/{id}",status="201"}`) {
		t.Errorf("requests_total not recorded as expected; body:\n%s", body)
	}
	if !strings.Contains(body, "go_goroutines") {
		t.Error("Go runtime collector not registered")
	}
}
