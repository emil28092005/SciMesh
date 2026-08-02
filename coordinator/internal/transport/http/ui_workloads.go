package http

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	"github.com/emil28092005/SciMesh/coordinator/internal/workloads"
)

// uiPortView is one sorted input/output port of a workload, rendered as
// pretty JSON on the library page.
type uiPortView struct {
	Name   string
	Schema string
}

// uiWorkloadView is the library page's view of one catalog workload.
type uiWorkloadView struct {
	Name         string
	Version      string
	Description  string
	Capabilities []string
	TrustModes   []string
	Determinism  string
	Verifier     string
	Enabled      bool
	Reduction    string
	Parameters   string
	Inputs       []uiPortView
	Outputs      []uiPortView
}

type uiWorkloadsView struct {
	Workloads []uiWorkloadView
}

var (
	workloadLibraryOnce sync.Once
	workloadLibraryView uiWorkloadsView
	workloadLibraryErr  error
)

func loadWorkloadLibrary() (uiWorkloadsView, error) {
	workloadLibraryOnce.Do(func() {
		catalog, err := workloads.Load()
		if err != nil {
			workloadLibraryErr = err
			return
		}
		view := uiWorkloadsView{Workloads: make([]uiWorkloadView, 0, len(catalog.Enabled()))}
		for _, item := range catalog.Enabled() {
			view.Workloads = append(view.Workloads, uiWorkloadView{
				Name:         item.Name,
				Version:      item.Version,
				Description:  item.Description,
				Capabilities: item.Capabilities,
				TrustModes:   item.TrustModes,
				Determinism:  item.Determinism,
				Verifier:     item.Verifier,
				Enabled:      item.Enabled,
				Reduction:    item.Reduction,
				Parameters:   prettyJSON(item.Parameters),
				Inputs:       portViews(item.Inputs),
				Outputs:      portViews(item.Outputs),
			})
		}
		workloadLibraryView = view
	})
	return workloadLibraryView, workloadLibraryErr
}

func prettyJSON(value any) string {
	if value == nil {
		return "{}"
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func portViews(ports map[string]any) []uiPortView {
	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, name)
	}
	sort.Strings(names)
	views := make([]uiPortView, 0, len(names))
	for _, name := range names {
		views = append(views, uiPortView{Name: name, Schema: prettyJSON(ports[name])})
	}
	return views
}

func (s *Server) handleUIWorkloads(w http.ResponseWriter, r *http.Request) {
	view, err := loadWorkloadLibrary()
	if err != nil {
		s.log.Error("load workload library", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderUI(w, "workloads.html", view)
}
