package usecase

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"mime"
	"strings"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// The preview is deliberately a diagnostic aid, never a full artifact
// viewer. These limits bound both memory use and storage reads.
const (
	previewMaxRows  = 30
	previewMaxBytes = 64 * 1024
)

// ArtifactPreviewView is the safe, bounded data rendered by the operator UI.
// It deliberately contains neither storage keys nor worker-local details.
type ArtifactPreviewView struct {
	JobID       string
	ArtifactID  string
	Filename    string
	Diagnostic  bool
	Previewable bool
	Reason      string
	Headers     []string
	Rows        [][]string
	Truncated   bool
	RowLimit    int
	ByteLimit   int64
}

// PreviewArtifact reads the beginning of a job-scoped CSV result. Partial
// results are diagnostic; a final result is available only after the reducer
// has persisted it as this job's completed result.
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
	// Another user's job (and not admin): report not-found, matching the
	// artifact-absent response so nothing about it leaks.
	if err := authorizeJobAccess(ctx, job); err != nil {
		return ArtifactPreviewView{}, domain.ErrArtifactNotFound
	}
	artifacts, err := p.read.ListArtifactsByJob(ctx, jobID)
	if err != nil {
		return ArtifactPreviewView{}, err
	}

	var artifact *domain.Artifact
	for i := range artifacts {
		if artifacts[i].ID == artifactID {
			artifact = &artifacts[i]
			break
		}
	}
	if artifact == nil || !previewableArtifact(*job, *artifact) {
		// Use one response for an unknown artifact, another job's artifact, and
		// an artifact that is not yet public. This avoids leaking its state.
		return ArtifactPreviewView{}, domain.ErrArtifactNotFound
	}

	view := ArtifactPreviewView{
		JobID:      jobID.String(),
		ArtifactID: artifact.ID.String(),
		Filename:   artifact.Filename,
		Diagnostic: artifact.Kind == domain.ArtifactPartialResult,
		RowLimit:   previewMaxRows,
		ByteLimit:  previewMaxBytes,
	}
	if !isCSVArtifact(artifact) {
		view.Reason = "This artifact is not a CSV file, so it cannot be shown as text here. Download it instead."
		return view, nil
	}
	if artifact.SizeBytes == 0 {
		view.Reason = "This artifact is empty."
		return view, nil
	}

	body, err := p.blobs.Open(ctx, artifact.StorageKey)
	if err != nil {
		return ArtifactPreviewView{}, err
	}
	defer func() { _ = body.Close() }()

	limited := &io.LimitedReader{R: body, N: previewMaxBytes}
	reader := csv.NewReader(limited)
	reader.FieldsPerRecord = -1 // a byte limit may end inside a record

	headers, err := reader.Read()
	if err != nil {
		view.Reason = "This artifact could not be read as CSV."
		return view, nil
	}
	view.Headers = append([]string(nil), headers...)
	view.Rows = make([][]string, 0, previewMaxRows)
	for len(view.Rows) < previewMaxRows {
		record, readErr := reader.Read()
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				view.Truncated = true
			}
			break
		}
		view.Rows = append(view.Rows, append([]string(nil), record...))
	}

	if artifact.SizeBytes > previewMaxBytes {
		view.Truncated = true
	} else if len(view.Rows) == previewMaxRows {
		if _, readErr := reader.Read(); readErr == nil {
			view.Truncated = true
		}
	}
	view.Previewable = true
	return view, nil
}

func previewableArtifact(job domain.Job, artifact domain.Artifact) bool {
	if artifact.Kind == domain.ArtifactPartialResult {
		return true
	}
	return artifact.Kind == domain.ArtifactFinalResult &&
		job.Status == domain.JobCompleted &&
		job.ResultArtifactID != nil &&
		*job.ResultArtifactID == artifact.ID
}

func isCSVArtifact(artifact *domain.Artifact) bool {
	mediaType, _, err := mime.ParseMediaType(artifact.ContentType)
	if err == nil && strings.EqualFold(mediaType, "text/csv") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(artifact.Filename), ".csv")
}
