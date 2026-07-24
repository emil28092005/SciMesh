// Package reducer contains deterministic, coordinator-side result reductions.
package reducer

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
)

var searchHeader = []string{"rank", "chembl_id", "canonical_smiles", "similarity"}

type similarityMatch struct {
	similarity float64
	id         string
	smiles     string
}

// ReduceSimilaritySearch streams worker-local top-k CSVs into the exact global
// top-k. Each partial is validated before it can affect the final artifact.
func ReduceSimilaritySearch(partials []io.Reader, parameters map[string]any) ([]byte, error) {
	topK, err := positiveInt(parameters["top_k"], 20)
	if err != nil {
		return nil, err
	}
	direction, err := thresholdDirection(parameters["threshold_direction"])
	if err != nil {
		return nil, err
	}
	h := &matchHeap{direction: direction}
	for _, partial := range partials {
		if err := readPartial(partial, direction, func(match similarityMatch) {
			if len(h.items) < topK {
				heapPush(h, match)
				return
			}
			if better(match, h.items[0], direction) {
				h.items[0] = match
				heapDown(h, 0)
			}
		}); err != nil {
			return nil, err
		}
	}

	matches := append([]similarityMatch(nil), h.items...)
	sort.Slice(matches, func(i, j int) bool { return better(matches[i], matches[j], direction) })
	var out bytes.Buffer
	writer := csv.NewWriter(&out)
	if err := writer.Write(searchHeader); err != nil {
		return nil, err
	}
	for index, match := range matches {
		if err := writer.Write([]string{
			strconv.Itoa(index + 1), match.id, match.smiles, fmt.Sprintf("%.6f", match.similarity),
		}); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func readPartial(input io.Reader, direction string, consume func(similarityMatch)) error {
	reader := csv.NewReader(input)
	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("read partial header: %w", err)
	}
	if !equalStrings(header, searchHeader) {
		return fmt.Errorf("partial result has an invalid CSV header")
	}
	var previous *similarityMatch
	for rank := 1; ; rank++ {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read partial row: %w", err)
		}
		if len(row) != len(searchHeader) || row[0] != strconv.Itoa(rank) {
			return fmt.Errorf("partial result has an invalid rank")
		}
		score, err := strconv.ParseFloat(row[3], 64)
		if err != nil || math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
			return fmt.Errorf("partial result has an invalid similarity")
		}
		match := similarityMatch{similarity: score, id: row[1], smiles: row[2]}
		if previous != nil && better(match, *previous, direction) {
			return fmt.Errorf("partial result is not sorted deterministically")
		}
		previous = &match
		consume(match)
	}
}

func positiveInt(value any, fallback int) (int, error) {
	if value == nil {
		return fallback, nil
	}
	switch n := value.(type) {
	case int:
		if n > 0 {
			return n, nil
		}
	case int64:
		if n > 0 && n <= math.MaxInt {
			return int(n), nil
		}
	case float64:
		if n > 0 && n == math.Trunc(n) && n <= math.MaxInt {
			return int(n), nil
		}
	}
	return 0, fmt.Errorf("top_k must be a positive integer")
}

func thresholdDirection(value any) (string, error) {
	if value == nil {
		return "greater", nil
	}
	direction, ok := value.(string)
	if !ok || (direction != "greater" && direction != "less") {
		return "", fmt.Errorf("threshold_direction must be greater or less")
	}
	return direction, nil
}

func better(left, right similarityMatch, direction string) bool {
	if left.similarity != right.similarity {
		if direction == "less" {
			return left.similarity < right.similarity
		}
		return left.similarity > right.similarity
	}
	if left.id != right.id {
		return left.id < right.id
	}
	return left.smiles < right.smiles
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// matchHeap keeps the worst retained match at index zero.
type matchHeap struct {
	items     []similarityMatch
	direction string
}

func heapPush(h *matchHeap, value similarityMatch) {
	h.items = append(h.items, value)
	for child := len(h.items) - 1; child > 0; {
		parent := (child - 1) / 2
		if !better(h.items[parent], h.items[child], h.direction) {
			break
		}
		h.items[parent], h.items[child] = h.items[child], h.items[parent]
		child = parent
	}
}

func heapDown(h *matchHeap, parent int) {
	for {
		child := parent*2 + 1
		if child >= len(h.items) {
			return
		}
		if right := child + 1; right < len(h.items) && better(h.items[child], h.items[right], h.direction) {
			child = right
		}
		if !better(h.items[parent], h.items[child], h.direction) {
			return
		}
		h.items[parent], h.items[child] = h.items[child], h.items[parent]
		parent = child
	}
}
