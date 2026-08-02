package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// ArtifactRepo implements usecase.ArtifactRepository on SQLite.
type ArtifactRepo struct {
	db *sql.DB
}

func NewArtifactRepo(db *sql.DB) *ArtifactRepo {
	return &ArtifactRepo{db: db}
}

const artifactColumns = `id, job_id, task_id, attempt, kind, filename, storage_key,
	content_type, size_bytes, sha256, created_at`

// scanArtifact maps one row onto a domain.Artifact.
func scanArtifact(row interface{ Scan(dest ...any) error }) (*domain.Artifact, error) {
	var (
		a    domain.Artifact
		kind string
	)
	var (
		taskID    sql.NullString
		attempt   sql.NullInt64
		createdAt sql.NullInt64
	)
	if err := row.Scan(
		&a.ID, &a.JobID, &taskID, &attempt, &kind, &a.Filename, &a.StorageKey,
		&a.ContentType, &a.SizeBytes, &a.SHA256, &createdAt,
	); err != nil {
		return nil, err
	}
	a.CreatedAt = decodeTime(createdAt.Int64)
	a.Kind = domain.ArtifactKind(kind)
	if taskID.Valid {
		if id, err := uuid.Parse(taskID.String); err == nil {
			a.TaskID = &id
		}
	}
	if attempt.Valid {
		value := int(attempt.Int64)
		a.Attempt = &value
	}
	return &a, nil
}

func (r *ArtifactRepo) Insert(ctx context.Context, a *domain.Artifact) error {
	_, err := conn(ctx, r.db).ExecContext(ctx, `
INSERT INTO artifacts (id, job_id, task_id, attempt, kind, filename, storage_key,
	content_type, size_bytes, sha256, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID.String(), a.JobID.String(), nullableUUID(a.TaskID), nullableInt(a.Attempt),
		string(a.Kind), a.Filename, a.StorageKey, a.ContentType, a.SizeBytes, a.SHA256,
		encodeTime(a.CreatedAt))
	return err
}

func (r *ArtifactRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	row := conn(ctx, r.db).QueryRowContext(ctx,
		"SELECT "+artifactColumns+" FROM artifacts WHERE id = ?", id.String())
	artifact, err := scanArtifact(row)
	return artifact, mapErrNoRows(err, domain.ErrArtifactNotFound)
}

func (r *ArtifactRepo) FindPartialResult(ctx context.Context, taskID uuid.UUID, attempt int) (*domain.Artifact, error) {
	row := conn(ctx, r.db).QueryRowContext(ctx,
		"SELECT "+artifactColumns+" FROM artifacts WHERE task_id = ? AND attempt = ? AND kind = ?",
		taskID.String(), attempt, string(domain.ArtifactPartialResult))
	artifact, err := scanArtifact(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return artifact, err
}

// nullableInt renders a nilable int as its value, or NULL.
func nullableInt(n *int) any {
	if n == nil {
		return nil
	}
	return *n
}
