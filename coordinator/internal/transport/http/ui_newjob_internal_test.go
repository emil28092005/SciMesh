package http

import (
	"strings"
	"testing"

	"github.com/emil28092005/SciMesh/coordinator/internal/workloads"
)

func TestNewJobPageCarriesWorkloadCatalogPayload(t *testing.T) {
	catalog, err := workloads.Load()
	if err != nil {
		t.Fatalf("load workload catalog: %v", err)
	}
	view := uiNewJobView{Payload: newJobPayload(catalog)}
	var builder strings.Builder
	if err := uiTemplates.ExecuteTemplate(&builder, "new-job.html", view); err != nil {
		t.Fatalf("render new-job page: %v", err)
	}
	page := builder.String()
	for _, expected := range []string{"const DATA=", "similarity-search", "molwt-filter", "descriptor-batch", "one_of", "query_id", "min_molwt", "skip_invalid", "upload_ready"} {
		if !strings.Contains(page, expected) {
			t.Errorf("new-job page is missing %q", expected)
		}
	}
}

func TestNewJobPayloadDeclaresUploadReadiness(t *testing.T) {
	catalog, err := workloads.Load()
	if err != nil {
		t.Fatalf("load workload catalog: %v", err)
	}
	payload := newJobPayload(catalog)
	text := string(payload)
	if !strings.Contains(text, `"upload_ready":false`) {
		t.Errorf("catalog payload must mark similarity-graph as not upload-ready")
	}
	if !strings.Contains(text, `"reduction":"top-k"`) {
		t.Errorf("catalog payload is missing the top-k reduction for search")
	}
	if !strings.Contains(text, `"reduction":"ordered-concat"`) {
		t.Errorf("catalog payload is missing the ordered-concat reduction for row workloads")
	}
}
