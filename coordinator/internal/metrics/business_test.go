package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)
	return rec.Body.String()
}

func TestBusinessCollectorEmitsGauges(t *testing.T) {
	m := New()
	m.RegisterBusiness(func(context.Context) (Stats, error) {
		return Stats{
			Tasks:   map[string]int{"pending": 3, "running": 1, "completed": 0},
			Jobs:    map[string]int{"running": 2},
			Workers: map[string]int{"online": 4},
		}, nil
	})

	body := scrape(t, m)
	for _, want := range []string{
		`scimesh_tasks{status="pending"} 3`,
		`scimesh_tasks{status="completed"} 0`,
		`scimesh_jobs{status="running"} 2`,
		`scimesh_workers{status="online"} 4`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q\n%s", want, body)
		}
	}
}

func TestBusinessCollectorSkipsOnError(t *testing.T) {
	m := New()
	m.RegisterBusiness(func(context.Context) (Stats, error) {
		return Stats{}, errors.New("db down")
	})
	if strings.Contains(scrape(t, m), "scimesh_tasks") {
		t.Error("a failed snapshot must emit no business samples")
	}
}
