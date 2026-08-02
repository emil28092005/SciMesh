package usecase

import (
	"testing"

	"github.com/emil28092005/SciMesh/coordinator/internal/workloads"
)

func TestUIParametersAreAllowlisted(t *testing.T) {
	catalog, err := workloads.Load()
	if err != nil {
		t.Fatal(err)
	}
	parameters := uiParameters(map[string]any{
		"query_smiles":         "CCO",
		"top_k":                float64(20),
		"internal_storage_key": "must-not-reach-browser",
		"nested":               map[string]any{"secret": "no"},
	}, catalog, "similarity-search")
	if len(parameters) != 2 {
		t.Fatalf("parameters = %#v, want only two allowlisted values", parameters)
	}
	if parameters[0] != (ParameterCard{Label: "Target SMILES", Value: "CCO"}) ||
		parameters[1] != (ParameterCard{Label: "Global top-k", Value: "20"}) {
		t.Fatalf("parameters = %#v", parameters)
	}
}

func TestUIParametersRenderEverySchemaDeclaredField(t *testing.T) {
	catalog, err := workloads.Load()
	if err != nil {
		t.Fatal(err)
	}
	parameters := uiParameters(map[string]any{
		"min_molwt":    100,
		"max_molwt":    600,
		"skip_invalid": true,
		"secret_key":   "no",
	}, catalog, "molwt-filter")
	if len(parameters) != 3 {
		t.Fatalf("parameters = %#v, want three declared values", parameters)
	}
	labels := map[string]bool{}
	for _, card := range parameters {
		labels[card.Label] = true
	}
	for _, expected := range []string{"Minimum molecular weight", "Maximum molecular weight", "Skip invalid molecules"} {
		if !labels[expected] {
			t.Errorf("missing parameter card %q in %#v", expected, parameters)
		}
	}
}
