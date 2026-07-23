package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestNewArtifact(t *testing.T) {
	jobID := uuid.New()
	taskID := uuid.New()
	a, err := NewArtifact(jobID, &taskID, ArtifactPartialResult, "result.csv", "text/csv", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if a.JobID != jobID || a.TaskID == nil || *a.TaskID != taskID {
		t.Error("ownership not recorded")
	}
	// Storage key is derived from the artifact id, never the filename — no path
	// traversal from a hostile "../.." name.
	if a.StorageKey != a.ID.String() {
		t.Errorf("storage key = %q, want the artifact id", a.StorageKey)
	}
	if a.SizeBytes != 0 || a.SHA256 != "" {
		t.Error("size and checksum are unknown until SetContent")
	}
}

func TestNewArtifactDefaultsContentType(t *testing.T) {
	a, err := NewArtifact(uuid.New(), nil, ArtifactInput, "data", "", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if a.ContentType != "application/octet-stream" {
		t.Errorf("content type = %q, want the default", a.ContentType)
	}
}

func TestNewArtifactRejectsBadInput(t *testing.T) {
	if _, err := NewArtifact(uuid.New(), nil, ArtifactInput, "", "text/csv", testNow); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty filename: err = %v, want ErrInvalidInput", err)
	}
	if _, err := NewArtifact(uuid.New(), nil, "", "f", "text/csv", testNow); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty kind: err = %v, want ErrInvalidInput", err)
	}
}

func TestArtifactSetContent(t *testing.T) {
	a, _ := NewArtifact(uuid.New(), nil, ArtifactShard, "shard-0.tsv", "text/csv", testNow)
	a.SetContent("deadbeef", 42)
	if a.SHA256 != "deadbeef" || a.SizeBytes != 42 {
		t.Error("SetContent must record checksum and size")
	}
}
