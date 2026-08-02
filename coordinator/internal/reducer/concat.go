// Package reducer contains deterministic, coordinator-side result reductions.
package reducer

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
)

// ReduceOrderedConcat concatenates worker partial tables in shard order into a
// single table with one header. Every partial must carry the same header as the
// first partial and rows of the same width; anything else fails the job closed.
func ReduceOrderedConcat(partials []io.Reader) ([]byte, error) {
	var out bytes.Buffer
	writer := csv.NewWriter(&out)
	var firstHeader []string
	for _, partial := range partials {
		reader := csv.NewReader(partial)
		header, err := reader.Read()
		if err != nil {
			return nil, fmt.Errorf("read partial header: %w", err)
		}
		if len(header) == 0 {
			return nil, fmt.Errorf("partial result has an empty header")
		}
		if firstHeader == nil {
			firstHeader = header
			if err := writer.Write(header); err != nil {
				return nil, err
			}
		} else if !equalStrings(header, firstHeader) {
			return nil, fmt.Errorf("partial result has an inconsistent header")
		}
		for {
			row, err := reader.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("read partial row: %w", err)
			}
			if len(row) != len(header) {
				return nil, fmt.Errorf("partial result has a row with an inconsistent width")
			}
			if err := writer.Write(row); err != nil {
				return nil, err
			}
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
