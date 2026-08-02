package reducer

import (
	"io"
	"strings"
	"testing"
)

func TestReduceOrderedConcatJoinsPartialsInOrderWithOneHeader(t *testing.T) {
	first := strings.NewReader("chembl_id,canonical_smiles\nA,CC\nB,CCC\n")
	second := strings.NewReader("chembl_id,canonical_smiles\nC,CCCC\n")

	output, err := ReduceOrderedConcat([]io.Reader{first, second})
	if err != nil {
		t.Fatal(err)
	}
	want := "chembl_id,canonical_smiles\nA,CC\nB,CCC\nC,CCCC\n"
	if string(output) != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestReduceOrderedConcatIsDeterministicAcrossInputOrder(t *testing.T) {
	left := strings.NewReader("id,rows\nA,1\nB,2\n")
	right := strings.NewReader("id,rows\nC,3\n")

	first, err := ReduceOrderedConcat([]io.Reader{left, right})
	if err != nil {
		t.Fatal(err)
	}
	left, right = strings.NewReader("id,rows\nA,1\nB,2\n"), strings.NewReader("id,rows\nC,3\n")
	second, err := ReduceOrderedConcat([]io.Reader{left, right})
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("concat is not deterministic: %q != %q", first, second)
	}
}

func TestReduceOrderedConcatRejectsInconsistentHeaders(t *testing.T) {
	first := strings.NewReader("a,b\n1,2\n")
	second := strings.NewReader("a,c\n1,2\n")
	if _, err := ReduceOrderedConcat([]io.Reader{first, second}); err == nil {
		t.Fatal("inconsistent headers must fail")
	}
}

func TestReduceOrderedConcatRejectsRaggedRows(t *testing.T) {
	partial := strings.NewReader("a,b\n1,2,3\n")
	if _, err := ReduceOrderedConcat([]io.Reader{partial}); err == nil {
		t.Fatal("ragged rows must fail")
	}
}

func TestReduceOrderedConcatEmptyPartials(t *testing.T) {
	output, err := ReduceOrderedConcat(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 0 {
		t.Fatalf("empty input must produce empty output, got %q", output)
	}
}
