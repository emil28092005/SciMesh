package usecase_test

import (
	"testing"

	"github.com/emil28092005/SciMesh/coordinator/internal/workloads"
)

// testCatalog loads the embedded workload catalog for usecase tests. The
// catalog is checked in and generated from the SDK library, so tests exercise
// the real validation contract.
func testCatalog() *workloads.Catalog {
	catalog, err := workloads.Load()
	if err != nil {
		panic(err)
	}
	return catalog
}

func TestEmbeddedCatalogLoadsAndValidatesSearchParameters(t *testing.T) {
	catalog := testCatalog()
	if err := catalog.ValidateParameters("similarity-search", map[string]any{
		"query_smiles": "CCO", "top_k": 10, "threshold_direction": "greater",
	}); err != nil {
		t.Fatalf("valid search parameters rejected: %v", err)
	}
	if err := catalog.ValidateParameters("molwt-filter", map[string]any{
		"min_molwt": 100, "max_molwt": 600, "skip_invalid": true,
	}); err != nil {
		t.Fatalf("valid molwt parameters rejected: %v", err)
	}
	if err := catalog.ValidateParameters("nope", map[string]any{}); err == nil {
		t.Error("unknown workload accepted")
	}
	if err := catalog.ValidateParameters("similarity-graph", map[string]any{"threshold": 0.7}); err != nil {
		t.Errorf("graph parameters rejected: %v", err)
	}
	if !catalog.UploadReady("molwt-filter") {
		t.Error("molwt-filter must be upload-ready")
	}
	if catalog.UploadReady("similarity-graph") {
		t.Error("similarity-graph must not be upload-ready")
	}
	if got := catalog.Reduction("similarity-search"); got != "top-k" {
		t.Errorf("search reduction = %q, want top-k", got)
	}
	if got := catalog.Reduction("descriptor-batch"); got != "ordered-concat" {
		t.Errorf("descriptor reduction = %q, want ordered-concat", got)
	}
	for name, parameters := range map[string]map[string]any{
		"both query fields":    {"query_id": "CHEMBL1", "query_smiles": "CCO"},
		"undeclared parameter": {"query_smiles": "CCO", "bogus": 1},
		"bad top_k":            {"query_smiles": "CCO", "top_k": -1},
		"bad enum":             {"query_smiles": "CCO", "threshold_direction": "sideways"},
		"missing query":        {},
		"non-integer top_k":    {"query_smiles": "CCO", "top_k": 1.5},
	} {
		if err := catalog.ValidateParameters("similarity-search", parameters); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
