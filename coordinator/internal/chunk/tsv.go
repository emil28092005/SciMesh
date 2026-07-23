// Package chunk splits a tabular input into deterministic shards. It is generic
// row splitting only — no workload semantics (SMILES, top-k) live here.
package chunk

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
)

// ErrNoRows is returned when the input has a header but no data rows: a job with
// zero tasks could never complete, so it is rejected at the source.
var ErrNoRows = fmt.Errorf("input has no data rows")

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
	if rowsPerShard <= 0 {
		return fmt.Errorf("rowsPerShard must be positive, got %d", rowsPerShard)
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
			buf.Write(header)
			buf.WriteByte('\n')
		}
		buf.Write(sc.Bytes())
		buf.WriteByte('\n')
		rows++

		if rows == rowsPerShard {
			if err := flush(); err != nil {
				return err
			}
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
