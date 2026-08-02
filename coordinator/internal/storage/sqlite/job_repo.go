package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// JobRepo implements usecase.JobRepository on SQLite.
type JobRepo struct {
	db *sql.DB
}

func NewJobRepo(db *sql.DB) *JobRepo {
	return &JobRepo{db: db}
}

const jobColumns = `id, workload, input_uri, parameters, status, created_at, completed_at,
	input_artifact_id, result_artifact_id, error_code, error_message, reducer_started_at, owner_id`

// scanJob maps one row onto a domain.Job. Scanned values follow the sqlite
// column order exactly: ids are TEXT, parameters JSON TEXT, timestamps unix
// nanoseconds (nullable), statuses plain strings.
func scanJob(row interface{ Scan(dest ...any) error }) (*domain.Job, error) {
	var (
		j      domain.Job
		status string
		params string
	)
	var (
		createdAt                              sql.NullInt64
		completedAt, reducerStartedAt          sql.NullInt64
		inputArtifact, resultArtifact, ownerID sql.NullString
		errorCode, errorMessage                sql.NullString
	)
	if err := row.Scan(
		&j.ID, &j.Workload, &j.InputURI, &params, &status, &createdAt,
		&completedAt, &inputArtifact, &resultArtifact, &errorCode, &errorMessage,
		&reducerStartedAt, &ownerID,
	); err != nil {
		return nil, err
	}
	if err := decodeJSON(params, &j.Parameters); err != nil {
		return nil, err
	}
	j.Status = domain.JobStatus(status)
	j.CreatedAt = decodeTime(createdAt.Int64)
	if completedAt.Valid {
		value := decodeTime(completedAt.Int64)
		j.CompletedAt = &value
	}
	if reducerStartedAt.Valid {
		value := decodeTime(reducerStartedAt.Int64)
		j.ReducerStartedAt = &value
	}
	if inputArtifact.Valid {
		if id, err := uuid.Parse(inputArtifact.String); err == nil {
			j.InputArtifactID = &id
		}
	}
	if resultArtifact.Valid {
		if id, err := uuid.Parse(resultArtifact.String); err == nil {
			j.ResultArtifactID = &id
		}
	}
	if ownerID.Valid {
		if id, err := uuid.Parse(ownerID.String); err == nil {
			j.OwnerID = &id
		}
	}
	if errorCode.Valid {
		j.ErrorCode = &errorCode.String
	}
	if errorMessage.Valid {
		j.ErrorMessage = &errorMessage.String
	}
	return &j, nil
}

// Insert runs inside the caller's transaction alongside the job's tasks.
func (r *JobRepo) Insert(ctx context.Context, j *domain.Job) error {
	_, err := conn(ctx, r.db).ExecContext(ctx, `
INSERT INTO jobs (id, workload, input_uri, parameters, status, created_at, owner_id)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		j.ID.String(), j.Workload, j.InputURI, encodeJSON(j.Parameters), string(j.Status),
		encodeTime(j.CreatedAt), nullableUUID(j.OwnerID))
	return err
}

func (r *JobRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	row := conn(ctx, r.db).QueryRowContext(ctx,
		"SELECT "+jobColumns+" FROM jobs WHERE id = ?", id.String())
	job, err := scanJob(row)
	return job, mapErrNoRows(err, domain.ErrJobNotFound)
}

func (r *JobRepo) ClaimReduction(ctx context.Context, id uuid.UUID, startedAt time.Time) (bool, error) {
	res, err := conn(ctx, r.db).ExecContext(ctx, `
UPDATE jobs SET reducer_started_at = ?
WHERE id = ? AND status = ? AND reducer_started_at IS NULL`,
		encodeTime(startedAt), id.String(), string(domain.JobReducing))
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	return affected == 1, err
}

func (r *JobRepo) CompleteWithResult(ctx context.Context, id, resultArtifactID uuid.UUID, completedAt time.Time) error {
	res, err := conn(ctx, r.db).ExecContext(ctx, `
UPDATE jobs SET status = ?, result_artifact_id = ?, completed_at = ?,
	reducer_started_at = NULL, error_code = NULL, error_message = NULL
WHERE id = ? AND status = ?`,
		string(domain.JobCompleted), resultArtifactID.String(), encodeTime(completedAt),
		id.String(), string(domain.JobReducing))
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrJobNotFound
	}
	return nil
}

func (r *JobRepo) FailReduction(ctx context.Context, id uuid.UUID, code, message string, completedAt time.Time) error {
	_, err := conn(ctx, r.db).ExecContext(ctx, `
UPDATE jobs SET status = ?, completed_at = ?, error_code = ?, error_message = ?,
	reducer_started_at = NULL
WHERE id = ? AND status = ?`,
		string(domain.JobFailed), encodeTime(completedAt), code, message,
		id.String(), string(domain.JobReducing))
	return err
}

func (r *JobRepo) UpdateStatus(ctx context.Context, id uuid.UUID,
	status domain.JobStatus, completedAt *time.Time) error {

	res, err := conn(ctx, r.db).ExecContext(ctx,
		"UPDATE jobs SET status = ?, completed_at = ? WHERE id = ?",
		string(status), encodeTimePtr(completedAt), id.String())
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrJobNotFound
	}
	return nil
}

// nullableUUID renders a nilable UUID as its text, or NULL.
func nullableUUID(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}
