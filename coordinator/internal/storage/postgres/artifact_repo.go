package postgres

import (
	"context"
	"errors"
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// ArtifactRepo implements usecase.ArtifactRepository.
type ArtifactRepo struct {
	pool *pgxpool.Pool
}

func NewArtifactRepo(pool *pgxpool.Pool) *ArtifactRepo {
	return &ArtifactRepo{pool: pool}
}

var _ usecase.ArtifactRepository = (*ArtifactRepo)(nil)

var artifactColumns = []string{
	"id", "job_id", "task_id", "kind", "filename", "storage_key",
	"content_type", "size_bytes", "sha256", "created_at",
}

func (r *ArtifactRepo) Insert(ctx context.Context, a *domain.Artifact) error {
	sql, args, err := psql.Insert("artifacts").
		Columns(artifactColumns...).
		Values(a.ID, a.JobID, a.TaskID, string(a.Kind), a.Filename, a.StorageKey,
			a.ContentType, a.SizeBytes, a.SHA256, a.CreatedAt).
		ToSql()
	if err != nil {
		return err
	}
	if _, err := conn(ctx, r.pool).Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("insert artifact: %w", err)
	}
	return nil
}

func (r *ArtifactRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	sql, args, err := psql.Select(artifactColumns...).
		From("artifacts").
		Where(sq.Eq{"id": id}).
		ToSql()
	if err != nil {
		return nil, err
	}

	var (
		a    domain.Artifact
		kind string
	)
	err = conn(ctx, r.pool).QueryRow(ctx, sql, args...).Scan(
		&a.ID, &a.JobID, &a.TaskID, &kind, &a.Filename, &a.StorageKey,
		&a.ContentType, &a.SizeBytes, &a.SHA256, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrArtifactNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get artifact: %w", err)
	}
	a.Kind = domain.ArtifactKind(kind)
	return &a, nil
}
