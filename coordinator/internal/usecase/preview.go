package usecase

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// previewMaxRows and previewMaxBytes bound how much of an artifact the
// diagnostic preview ever reads or renders: a partial shard CSV can be large,
// and this is a diagnostic aid, not a viewer for the full file.
const (
	previewMaxRows  = 30
	previewMaxBytes = 64 * 1024
)

// ArtifactPreviewView is what the UI renders for a diagnostic CSV preview. It
// never carries a storage path, database error, or worker-local detail.
type ArtifactPreviewView struct {
	JobID       string
	ArtifactID  string
	Filename    string
	Previewable bool
	Reason      string
	Headers     []string
	Rows        [][]string
	Truncated   bool
	RowLimit    int
	ByteLimit   int64
}

// PreviewArtifact renders at most the first previewMaxRows rows of a CSV
// artifact, reading at most previewMaxBytes from storage. It reuses the same
// job-scoped, downloadable-artifact rule as the download proxy so an artifact
// ID from another job is never previewable.
type PreviewArtifact struct {
	read  UIReadRepository
	blobs BlobStore
}

func NewPreviewArtifact(read UIReadRepository, blobs BlobStore) *PreviewArtifact {
	return &PreviewArtifact{read: read, blobs: blobs}
}

func (p *PreviewArtifact) Execute(ctx context.Context, jobID, artifactID uuid.UUID) (ArtifactPreviewView, error) {
	job, err := p.read.GetJob(ctx, jobID)
	if err != nil {
		return ArtifactPreviewView{}, err
	}
	tasks, err := p.read.ListTasksByJob(ctx, jobID)
	if err != nil {
		return ArtifactPreviewView{}, err
	}
	// Same status derivation the dashboard uses, so a final artifact previews
	// exactly when it would also be offered for download.
	status := jobCard(*job, tasks).Status

	artifacts, err := p.read.ListArtifactsByJob(ctx, jobID)
	if err != nil {
		return ArtifactPreviewView{}, err
	}
	var art *domain.Artifact
	for i := range artifacts {
		if artifacts[i].ID == artifactID {
			art = &artifacts[i]
			break
		}
	}
	if art == nil {
		return ArtifactPreviewView{}, domain.ErrArtifactNotFound
	}
	downloadable := art.Kind == domain.ArtifactPartialResult ||
		(art.Kind == domain.ArtifactFinalResult && status == string(domain.JobCompleted))
	if !downloadable {
		return ArtifactPreviewView{}, domain.ErrArtifactNotFound
	}

	view := ArtifactPreviewView{
		JobID:      jobID.String(),
		ArtifactID: art.ID.String(),
		Filename:   art.Filename,
		RowLimit:   previewMaxRows,
		ByteLimit:  previewMaxBytes,
	}
	if !isCSVArtifact(art) {
		view.Reason = "This artifact is not a CSV file, so it cannot be shown as text here. Download it instead."
		return view, nil
	}
	if art.SizeBytes == 0 {
		view.Reason = "This artifact is empty."
		return view, nil
	}

	rc, err := p.blobs.Open(ctx, art.StorageKey)
	if err != nil {
		return ArtifactPreviewView{}, err
	}
	defer func() { _ = rc.Close() }()

	// LimitedReader caps the bytes read from storage regardless of how many
	// rows are found within that window — the artifact is never loaded whole.
	limited := &io.LimitedReader{R: rc, N: previewMaxBytes}
	reader := csv.NewReader(limited)
	reader.FieldsPerRecord = -1 // a byte-limited cut mid-row must not look like a schema error

	header, err := reader.Read()
	if err != nil {
		view.Reason = "This artifact could not be read as CSV."
		return view, nil
	}
	view.Headers = append([]string(nil), header...)

	rows := make([][]string, 0, previewMaxRows)
	for len(rows) < previewMaxRows {
		record, err := reader.Read()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// Malformed content further into the stream: keep what parsed
				// cleanly and say the preview stopped early.
				view.Truncated = true
			}
			break
		}
		rows = append(rows, append([]string(nil), record...))
	}
	view.Rows = rows

	if art.SizeBytes > previewMaxBytes {
		view.Truncated = true
	} else if len(rows) == previewMaxRows {
		if _, err := reader.Read(); err == nil {
			view.Truncated = true
		}
	}
	view.Previewable = true
	return view, nil
}

func isCSVArtifact(a *domain.Artifact) bool {
	if a.ContentType == "text/csv" {
		return true
	}
	return strings.HasSuffix(strings.ToLower(a.Filename), ".csv")
}
