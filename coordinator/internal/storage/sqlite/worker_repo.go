package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/domain"
)

// WorkerRepo implements usecase.WorkerRepository on SQLite.
type WorkerRepo struct {
	db *sql.DB
}

func NewWorkerRepo(db *sql.DB) *WorkerRepo {
	return &WorkerRepo{db: db}
}

const workerColumns = `id, name, capabilities, status, owner_id, trust_level, last_heartbeat_at, created_at, updated_at`

// scanWorker maps one row onto a domain.Worker.
func scanWorker(row interface{ Scan(dest ...any) error }) (*domain.Worker, error) {
	var (
		w      domain.Worker
		status string
		trust  string
		caps   string
	)
	var (
		ownerID                         sql.NullString
		lastHeartbeat, created, updated sql.NullInt64
	)
	if err := row.Scan(
		&w.ID, &w.Name, &caps, &status, &ownerID, &trust,
		&lastHeartbeat, &created, &updated,
	); err != nil {
		return nil, err
	}
	w.LastHeartbeatAt = decodeTime(lastHeartbeat.Int64)
	w.CreatedAt = decodeTime(created.Int64)
	w.UpdatedAt = decodeTime(updated.Int64)
	if err := decodeJSON(caps, &w.Capabilities); err != nil {
		return nil, err
	}
	w.Status = domain.WorkerStatus(status)
	w.TrustLevel = domain.WorkerTrust(trust)
	if ownerID.Valid {
		if id, err := uuid.Parse(ownerID.String); err == nil {
			w.OwnerID = &id
		}
	}
	return &w, nil
}

func (r *WorkerRepo) Insert(ctx context.Context, w *domain.Worker) error {
	_, err := conn(ctx, r.db).ExecContext(ctx, `
INSERT INTO workers (id, name, capabilities, status, owner_id, trust_level,
	last_heartbeat_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID.String(), w.Name, encodeJSON(w.Capabilities), string(w.Status),
		nullableUUID(w.OwnerID), string(w.TrustLevel),
		encodeTime(w.LastHeartbeatAt), encodeTime(w.CreatedAt), encodeTime(w.UpdatedAt))
	return err
}

func (r *WorkerRepo) Get(ctx context.Context, id uuid.UUID) (*domain.Worker, error) {
	row := conn(ctx, r.db).QueryRowContext(ctx,
		"SELECT "+workerColumns+" FROM workers WHERE id = ?", id.String())
	worker, err := scanWorker(row)
	return worker, mapErrNoRows(err, domain.ErrWorkerNotFound)
}

func (r *WorkerRepo) Touch(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := conn(ctx, r.db).ExecContext(ctx,
		"UPDATE workers SET last_heartbeat_at = ?, status = ?, updated_at = ? WHERE id = ?",
		encodeTime(at), string(domain.WorkerOnline), encodeTime(at), id.String())
	return err
}

func (r *WorkerRepo) MarkStaleOffline(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := conn(ctx, r.db).ExecContext(ctx,
		"UPDATE workers SET status = ?, updated_at = ? WHERE last_heartbeat_at < ? AND status <> ?",
		string(domain.WorkerOffline), encodeTime(cutoff), encodeTime(cutoff),
		string(domain.WorkerOffline))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *WorkerRepo) SetTrust(ctx context.Context, id uuid.UUID, trust domain.WorkerTrust) error {
	res, err := conn(ctx, r.db).ExecContext(ctx,
		"UPDATE workers SET trust_level = ?, updated_at = ? WHERE id = ?",
		string(trust), encodeTime(time.Now()), id.String())
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrWorkerNotFound
	}
	return nil
}
