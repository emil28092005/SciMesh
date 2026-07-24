package usecase

import "testing"

func TestUIParametersAreAllowlisted(t *testing.T) {
	parameters := uiParameters(map[string]any{
		"query_smiles":         "CCO",
		"top_k":                float64(20),
		"internal_storage_key": "must-not-reach-browser",
		"nested":               map[string]any{"secret": "no"},
	})
	if len(parameters) != 2 {
		t.Fatalf("parameters = %#v, want only two allowlisted values", parameters)
	}
	if parameters[0] != (ParameterCard{Label: "Target SMILES", Value: "CCO"}) ||
		parameters[1] != (ParameterCard{Label: "Global top-k", Value: "20"}) {
		t.Fatalf("parameters = %#v", parameters)
	}
}
