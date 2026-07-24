// Package chunk splits a tabular input into deterministic shards. It is generic
// row splitting only — no workload semantics (SMILES, top-k) live here.
package chunk

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
)

// ErrNoRows is returned when the input has a header but no data rows: a job with
// zero tasks could never complete, so it is rejected at the source.
var ErrNoRows = fmt.Errorf("input has no data rows")

// maxShardBytes bounds the coordinator memory used by one in-progress shard.
// The uploaded file may be much larger: it is first stored on disk, then split
// in small bounded pieces. Operators can lower rowsPerShard when this limit is
// reached rather than exhausting the coordinator process.
const maxShardBytes = 64 << 20 // 64 MiB

// SplitTSV reads a header-plus-rows text stream and cuts it into shards of at
// most rowsPerShard data rows. Every shard repeats the header, so a worker can
// parse its shard in isolation. emit is called once per shard, in order, with a
// reader over that shard's bytes; the reader is valid only for the duration of
// the call.
//
// Splitting is deterministic: the same input and rowsPerShard always produce the
// same shards, byte for byte — which is what lets chunk_index refer to a stable
// piece and makes a re-run reproducible.
//
// Only one shard is buffered at a time, so memory is bounded by shard size (a
// worker-sized slice of the data), not by the size of the whole dataset.
func SplitTSV(r io.Reader, rowsPerShard int, emit func(index int, shard io.Reader) error) error {
	return splitTSVLimit(r, rowsPerShard, 0, nil, emit)
}

// SplitTSVLimit behaves like SplitTSV but emits no more than maxRows data rows.
// A maxRows value of zero means unlimited. This lets an operator make a small,
// representative pipeline check without materialising a second dataset file.
func SplitTSVLimit(r io.Reader, rowsPerShard, maxRows int, emit func(index int, shard io.Reader) error) error {
	return splitTSVLimit(r, rowsPerShard, maxRows, nil, emit)
}

// SplitChEMBLTSVLimit is the coordinator's scientific-upload splitter. It
// validates the two columns every local SciMesh workload requires before any
// shard task is persisted, while generic SplitTSV remains reusable for future
// non-chemistry workloads.
func SplitChEMBLTSVLimit(r io.Reader, rowsPerShard, maxRows int, emit func(index int, shard io.Reader) error) error {
	return splitTSVLimit(r, rowsPerShard, maxRows, validateChEMBLHeader, emit)
}

func splitTSVLimit(r io.Reader, rowsPerShard, maxRows int, validateHeader func([]byte) error, emit func(index int, shard io.Reader) error) error {
	if rowsPerShard <= 0 {
		return fmt.Errorf("rowsPerShard must be positive, got %d", rowsPerShard)
	}
	if maxRows < 0 {
		return fmt.Errorf("maxRows must be non-negative, got %d", maxRows)
	}

	sc := bufio.NewScanner(r)
	// Allow long lines: a SMILES row can be far wider than bufio's 64 KB default.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return fmt.Errorf("read header: %w", err)
		}
		return ErrNoRows // completely empty input
	}
	header := append([]byte(nil), sc.Bytes()...)
	if validateHeader != nil {
		if err := validateHeader(header); err != nil {
			return err
		}
	}

	var (
		buf   bytes.Buffer
		rows  int
		index int
	)

	// flush emits the buffered shard and resets for the next one.
	flush := func() error {
		if err := emit(index, bytes.NewReader(buf.Bytes())); err != nil {
			return err
		}
		index++
		buf.Reset()
		rows = 0
		return nil
	}

	for sc.Scan() {
		if rows == 0 {
			if len(header)+1 > maxShardBytes {
				return fmt.Errorf("TSV header exceeds maximum shard size of %d bytes", maxShardBytes)
			}
			buf.Write(header)
			buf.WriteByte('\n')
		}
		if buf.Len()+len(sc.Bytes())+1 > maxShardBytes {
			return fmt.Errorf("shard exceeds maximum size of %d bytes; lower rowsPerShard", maxShardBytes)
		}
		buf.Write(sc.Bytes())
		buf.WriteByte('\n')
		rows++

		if rows == rowsPerShard {
			if err := flush(); err != nil {
				return err
			}
		}
		if maxRows > 0 && index*rowsPerShard+rows == maxRows {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read rows: %w", err)
	}

	// A partial final shard still has to go out.
	if rows > 0 {
		if err := flush(); err != nil {
			return err
		}
	}

	if index == 0 {
		return ErrNoRows // header only, no data
	}
	return nil
}

func validateChEMBLHeader(header []byte) error {
	seen := make(map[string]struct{})
	for _, field := range strings.Split(strings.TrimPrefix(string(header), "\ufeff"), "\t") {
		seen[field] = struct{}{}
	}
	if _, ok := seen["chembl_id"]; !ok {
		return fmt.Errorf("TSV is missing required column chembl_id")
	}
	if _, ok := seen["canonical_smiles"]; !ok {
		return fmt.Errorf("TSV is missing required column canonical_smiles")
	}
	return nil
}
