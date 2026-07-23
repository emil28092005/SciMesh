package domain

import (
	"time"

	"github.com/google/uuid"
)

type ArtifactKind string

const (
	ArtifactInput         ArtifactKind = "input"
	ArtifactShard         ArtifactKind = "shard"
	ArtifactPartialResult ArtifactKind = "partial_result"
	ArtifactFinalResult   ArtifactKind = "final_result"
	ArtifactLog           ArtifactKind = "log"
)

// Artifact is a durable file the coordinator owns, described by its metadata.
// The bytes live in blob storage under StorageKey; this struct is what the
// database persists and what every other layer reasons about.
type Artifact struct {
	ID          uuid.UUID
	JobID       uuid.UUID
	TaskID      *uuid.UUID // nil for a job-level input
	Kind        ArtifactKind
	Filename    string
	StorageKey  string
	ContentType string
	SizeBytes   int64
	SHA256      string
	CreatedAt   time.Time
}

// NewArtifact begins an artifact record. Size and checksum are unknown until the
// bytes have been streamed to storage, so they are filled in later by SetContent.
//
// StorageKey is derived from a fresh UUID, never from the client-supplied
// filename — that is what stops a "../../etc/passwd" filename from escaping the
// storage directory.
func NewArtifact(jobID uuid.UUID, taskID *uuid.UUID, kind ArtifactKind,
	filename, contentType string, now time.Time) (*Artifact, error) {

	if filename == "" || kind == "" {
		return nil, ErrInvalidInput
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	id := uuid.New()
	return &Artifact{
		ID:          id,
		JobID:       jobID,
		TaskID:      taskID,
		Kind:        kind,
		Filename:    filename,
		StorageKey:  id.String(),
		ContentType: contentType,
		CreatedAt:   now,
	}, nil
}

// SetContent records the size and checksum measured while streaming the bytes
// into storage. Both are computed by the coordinator, never trusted from the
// client — the whole point of owning the artifact.
func (a *Artifact) SetContent(sha256 string, size int64) {
	a.SHA256 = sha256
	a.SizeBytes = size
}
