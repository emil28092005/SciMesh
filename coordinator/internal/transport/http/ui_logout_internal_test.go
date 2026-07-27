package http

import (
	"bytes"
	"strings"
	"testing"

	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

func render(t *testing.T, name string, data any) string {
	t.Helper()
	var buf bytes.Buffer
	if err := uiTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return buf.String()
}

func TestDashboardLogoutOnlyInSession(t *testing.T) {
	withSession := render(t, "dashboard.html", usecase.DashboardView{Session: &usecase.SessionView{Role: "admin"}})
	if !strings.Contains(withSession, "/ui/logout") || !strings.Contains(withSession, "Log out") {
		t.Error("dashboard must show a logout control in session mode")
	}

	noSession := render(t, "dashboard.html", usecase.DashboardView{})
	if strings.Contains(noSession, "/ui/logout") {
		t.Error("dashboard must not show logout under basic auth (no session)")
	}
}

func TestJobLogoutOnlyInSession(t *testing.T) {
	withSession := render(t, "job.html", usecase.JobDetailView{Session: &usecase.SessionView{Role: "user"}})
	if !strings.Contains(withSession, "/ui/logout") {
		t.Error("job page must show a logout control in session mode")
	}

	noSession := render(t, "job.html", usecase.JobDetailView{})
	if strings.Contains(noSession, "/ui/logout") {
		t.Error("job page must not show logout under basic auth (no session)")
	}
}
