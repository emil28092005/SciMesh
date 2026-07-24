package chunk

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// collect runs SplitTSV and returns every shard as a string.
func collect(t *testing.T, input string, rowsPerShard int) []string {
	t.Helper()
	var shards []string
	err := SplitTSV(strings.NewReader(input), rowsPerShard, func(index int, shard io.Reader) error {
		b, _ := io.ReadAll(shard)
		if index != len(shards) {
			t.Fatalf("emit index = %d, want %d (out of order)", index, len(shards))
		}
		shards = append(shards, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("SplitTSV: %v", err)
	}
	return shards
}

func TestSplitCountsShardsAndRepeatsHeader(t *testing.T) {
	input := "id\tsmiles\nA\tCC\nB\tCCC\nC\tCCCC\nD\tCCCCC\nE\tCCCCCC\n"
	shards := collect(t, input, 2)

	if len(shards) != 3 { // 5 rows / 2 per shard = ceil = 3
		t.Fatalf("got %d shards, want 3", len(shards))
	}
	for i, s := range shards {
		if !strings.HasPrefix(s, "id\tsmiles\n") {
			t.Errorf("shard %d missing header: %q", i, s)
		}
	}
	if shards[0] != "id\tsmiles\nA\tCC\nB\tCCC\n" {
		t.Errorf("shard 0 = %q", shards[0])
	}
	if shards[2] != "id\tsmiles\nE\tCCCCCC\n" { // partial final shard
		t.Errorf("shard 2 = %q", shards[2])
	}
}

func TestSplitExactMultipleHasNoEmptyTrailingShard(t *testing.T) {
	input := "h\nr1\nr2\nr3\nr4\n"
	shards := collect(t, input, 2)
	if len(shards) != 2 { // exactly 4/2, no empty third shard
		t.Fatalf("got %d shards, want 2", len(shards))
	}
}

func TestSplitIsDeterministic(t *testing.T) {
	input := "h\n" + strings.Repeat("row\n", 100)
	a := collect(t, input, 7)
	b := collect(t, input, 7)
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Error("two runs produced different shards")
	}
}

func TestSplitRejectsHeaderOnly(t *testing.T) {
	err := SplitTSV(strings.NewReader("id\tsmiles\n"), 10, func(int, io.Reader) error { return nil })
	if !errors.Is(err, ErrNoRows) {
		t.Errorf("err = %v, want ErrNoRows", err)
	}
}

func TestSplitRejectsEmptyInput(t *testing.T) {
	err := SplitTSV(strings.NewReader(""), 10, func(int, io.Reader) error { return nil })
	if !errors.Is(err, ErrNoRows) {
		t.Errorf("err = %v, want ErrNoRows", err)
	}
}

func TestSplitRejectsNonPositiveSize(t *testing.T) {
	err := SplitTSV(strings.NewReader("h\nr\n"), 0, func(int, io.Reader) error { return nil })
	if err == nil {
		t.Error("expected an error for rowsPerShard = 0")
	}
}

func TestSplitPropagatesEmitError(t *testing.T) {
	boom := errors.New("boom")
	err := SplitTSV(strings.NewReader("h\nr1\nr2\n"), 1, func(int, io.Reader) error { return boom })
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestSplitSingleShardWhenSizeExceedsRows(t *testing.T) {
	shards := collect(t, "h\nr1\nr2\n", 100)
	if len(shards) != 1 {
		t.Fatalf("got %d shards, want 1", len(shards))
	}
	if shards[0] != "h\nr1\nr2\n" {
		t.Errorf("shard 0 = %q", shards[0])
	}
}

func TestSplitLimitUsesOnlyLeadingDataRows(t *testing.T) {
	input := "h\nr1\nr2\nr3\nr4\nr5\n"
	var shards []string
	err := SplitTSVLimit(strings.NewReader(input), 2, 3, func(_ int, shard io.Reader) error {
		b, _ := io.ReadAll(shard)
		shards = append(shards, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(shards, ""), "h\nr1\nr2\nh\nr3\n"; got != want {
		t.Errorf("limited shards = %q, want %q", got, want)
	}
}

func TestChEMBLSplitRejectsMissingRequiredColumns(t *testing.T) {
	err := SplitChEMBLTSVLimit(strings.NewReader("id\tsmiles\nA\tCC\n"), 1, 0,
		func(int, io.Reader) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "chembl_id") {
		t.Errorf("err = %v, want missing-column error", err)
	}
}

// The scanned bytes are reused by bufio; the shard buffer must copy them, or a
// later row would corrupt an earlier one. This guards that copy.
func TestSplitDoesNotAliasScannerBuffer(t *testing.T) {
	var got bytes.Buffer
	_ = SplitTSV(strings.NewReader("h\naaaa\nbbbb\n"), 2, func(_ int, shard io.Reader) error {
		_, _ = io.Copy(&got, shard)
		return nil
	})
	if want := "h\naaaa\nbbbb\n"; got.String() != want {
		t.Errorf("got %q, want %q", got.String(), want)
	}
}
