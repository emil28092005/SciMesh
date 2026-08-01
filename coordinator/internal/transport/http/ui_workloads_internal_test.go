package http

import (
	"strings"
	"testing"
)

func TestWorkloadLibraryLoadsAndListsEveryWorkload(t *testing.T) {
	view, err := loadWorkloadLibrary()
	if err != nil {
		t.Fatalf("load workload library: %v", err)
	}
	names := make(map[string]bool)
	for _, workload := range view.Workloads {
		if workload.Name == "" || workload.Version == "" {
			t.Errorf("workload with empty name or version: %+v", workload)
		}
		if workload.Description == "" {
			t.Errorf("workload %s has no description", workload.Name)
		}
		if workload.Parameters == "" {
			t.Errorf("workload %s has no parameter schema", workload.Name)
		}
		if len(workload.Inputs) == 0 || len(workload.Outputs) == 0 {
			t.Errorf("workload %s has no input or output ports", workload.Name)
		}
		names[workload.Name] = true
	}
	for _, expected := range []string{"similarity-search", "similarity-graph", "descriptor-batch", "molwt-filter"} {
		if !names[expected] {
			t.Errorf("workload library is missing %s", expected)
		}
	}
}

func TestWorkloadLibraryPageRendersWorkloads(t *testing.T) {
	view, err := loadWorkloadLibrary()
	if err != nil {
		t.Fatalf("load workload library: %v", err)
	}
	var builder strings.Builder
	if err := uiTemplates.ExecuteTemplate(&builder, "workloads.html", view); err != nil {
		t.Fatalf("render workloads page: %v", err)
	}
	page := builder.String()
	for _, expected := range []string{"Workload library", "descriptor-batch", "molwt-filter", "byte_exact", "exact-artifact@1"} {
		if !strings.Contains(page, expected) {
			t.Errorf("workloads page is missing %q", expected)
		}
	}
}
