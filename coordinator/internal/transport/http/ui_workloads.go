package http

import (
	"embed"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
)

//go:embed workloads.json
var workloadLibraryFile embed.FS

// uiWorkloadLibrary is the catalog written by `scimesh workload export`. The
// coordinator never evaluates the schemas in it; it is presentation metadata
// for the operator UI, kept in sync by `make workloads-export`.
type uiWorkloadLibrary struct {
	SchemaVersion int             `json:"schema_version"`
	GeneratedBy   string          `json:"generated_by"`
	Workloads     []uiWorkloadRaw `json:"workloads"`
}

type uiWorkloadRaw struct {
	Name         string                    `json:"name"`
	Version      string                    `json:"version"`
	Description  string                    `json:"description"`
	Capabilities []string                  `json:"capabilities"`
	TrustModes   []string                  `json:"trust_modes"`
	Determinism  string                    `json:"determinism"`
	Verifier     string                    `json:"verifier"`
	Enabled      bool                      `json:"enabled"`
	Parameters   map[string]any            `json:"parameters_schema"`
	Inputs       map[string]map[string]any `json:"inputs"`
	Outputs      map[string]map[string]any `json:"outputs"`
}

type uiPortView struct {
	Name   string
	Schema string
}

type uiWorkloadView struct {
	Name         string
	Version      string
	Description  string
	Capabilities []string
	TrustModes   []string
	Determinism  string
	Verifier     string
	Enabled      bool
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
		data, err := workloadLibraryFile.ReadFile("workloads.json")
		if err != nil {
			workloadLibraryErr = err
			return
		}
		var raw uiWorkloadLibrary
		if err := json.Unmarshal(data, &raw); err != nil {
			workloadLibraryErr = err
			return
		}
		view := uiWorkloadsView{Workloads: make([]uiWorkloadView, 0, len(raw.Workloads))}
		for _, item := range raw.Workloads {
			view.Workloads = append(view.Workloads, uiWorkloadView{
				Name:         item.Name,
				Version:      item.Version,
				Description:  item.Description,
				Capabilities: item.Capabilities,
				TrustModes:   item.TrustModes,
				Determinism:  item.Determinism,
				Verifier:     item.Verifier,
				Enabled:      item.Enabled,
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

func portViews(ports map[string]map[string]any) []uiPortView {
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
