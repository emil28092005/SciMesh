package reducer

import (
	"io"
	"strings"
	"testing"
)

func TestReduceSimilaritySearchKeepsExactCrossShardRanking(t *testing.T) {
	first := strings.NewReader("rank,chembl_id,canonical_smiles,similarity\n1,B,CCC,0.50000048\n2,C,CCCC,0.1\n")
	second := strings.NewReader("rank,chembl_id,canonical_smiles,similarity\n1,A,CC,0.50000049\n")

	output, err := ReduceSimilaritySearch([]io.Reader{first, second}, map[string]any{"top_k": 2})
	if err != nil {
		t.Fatal(err)
	}
	want := "rank,chembl_id,canonical_smiles,similarity\n1,A,CC,0.500000\n2,B,CCC,0.500000\n"
	if string(output) != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestReduceSimilaritySearchSupportsLeastSimilarDirection(t *testing.T) {
	partial := strings.NewReader("rank,chembl_id,canonical_smiles,similarity\n1,A,CC,0.1\n2,B,CCC,0.8\n")
	output, err := ReduceSimilaritySearch([]io.Reader{partial}, map[string]any{
		"top_k": 1, "threshold_direction": "less",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(output), "rank,chembl_id,canonical_smiles,similarity\n1,A,CC,0.100000\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestReduceSimilaritySearchRejectsMalformedPartial(t *testing.T) {
	partial := strings.NewReader("rank,chembl_id,canonical_smiles,similarity\n2,A,CC,0.1\n")
	if _, err := ReduceSimilaritySearch([]io.Reader{partial}, nil); err == nil {
		t.Fatal("expected malformed rank error")
	}
}
