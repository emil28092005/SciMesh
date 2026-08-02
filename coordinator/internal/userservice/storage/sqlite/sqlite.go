// Package sqlite implements the userservice repository ports on an embedded
// SQLite database, mirroring the coordinator's single-binary storage choice.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/domain"
	"github.com/emil28092005/SciMesh/coordinator/internal/userservice/usecase"
)

const userColumns = `id, email, password_hash, role, verified, created_at, updated_at`

// scanUser maps one row onto a domain.User.
func scanUser(row interface{ Scan(dest ...any) error }) (*domain.User, error) {
	var (
		u        domain.User
		role     string
		verified int64
	)
	var created, updated sql.NullInt64
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &role, &verified, &created, &updated); err != nil {
		return nil, err
	}
	u.Role = domain.Role(role)
	u.Verified = verified != 0
	u.CreatedAt = decodeTime(created.Int64)
	u.UpdatedAt = decodeTime(updated.Int64)
	return &u, nil
}

// UserRepo implements usecase.UserRepository on SQLite.
type UserRepo struct {
	db *sql.DB
}

func NewUserRepo(db *sql.DB) *UserRepo {
	return &UserRepo{db: db}
}

func (r *UserRepo) Insert(ctx context.Context, u *domain.User) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO users (id, email, password_hash, role, verified, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.ID.String(), u.Email, u.PasswordHash, string(u.Role), boolInt(u.Verified),
		u.CreatedAt.UnixNano(), u.UpdatedAt.UnixNano())
	if err != nil && isUnique(err) {
		return usecase.ErrEmailExists
	}
	return err
}

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	return r.getBy(ctx, "email = ?", email)
}

func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return r.getBy(ctx, "id = ?", id.String())
}

func (r *UserRepo) getBy(ctx context.Context, clause string, arg any) (*domain.User, error) {
	// #nosec G202 -- clause is an internal constant, never user input.
	row := r.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE "+clause, arg)
	user, err := scanUser(row)
	return user, mapErrNoRows(err, usecase.ErrUserNotFound)
}

func (r *UserRepo) SetVerified(ctx context.Context, id uuid.UUID, verified bool) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE users SET verified = ?, updated_at = ? WHERE id = ?",
		boolInt(verified), time.Now().UnixNano(), id.String())
	if err != nil {
		return err
	}
	return rowsAffectedOrNotFound(res, usecase.ErrUserNotFound)
}

func (r *UserRepo) SetRole(ctx context.Context, id uuid.UUID, role domain.Role) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE users SET role = ?, updated_at = ? WHERE id = ?",
		string(role), time.Now().UnixNano(), id.String())
	if err != nil {
		return err
	}
	return rowsAffectedOrNotFound(res, usecase.ErrUserNotFound)
}

const workerKeyColumns = `id, user_id, name, token_hash, prefix, created_at, last_used_at, revoked_at`

func scanWorkerKey(row interface{ Scan(dest ...any) error }) (*domain.WorkerKey, error) {
	var (
		k        domain.WorkerKey
		lastUsed sql.NullInt64
		revoked  sql.NullInt64
		created  sql.NullInt64
	)
	if err := row.Scan(&k.ID, &k.UserID, &k.Name, &k.TokenHash, &k.Prefix, &created, &lastUsed, &revoked); err != nil {
		return nil, err
	}
	k.CreatedAt = decodeTime(created.Int64)
	if lastUsed.Valid {
		value := decodeTime(lastUsed.Int64)
		k.LastUsedAt = &value
	}
	if revoked.Valid {
		value := decodeTime(revoked.Int64)
		k.RevokedAt = &value
	}
	return &k, nil
}

// WorkerKeyRepo implements usecase.WorkerKeyRepository on SQLite.
type WorkerKeyRepo struct {
	db *sql.DB
}

func NewWorkerKeyRepo(db *sql.DB) *WorkerKeyRepo {
	return &WorkerKeyRepo{db: db}
}

func (r *WorkerKeyRepo) Insert(ctx context.Context, k *domain.WorkerKey) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO worker_keys (id, user_id, name, token_hash, prefix, created_at, last_used_at, revoked_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID.String(), k.UserID.String(), k.Name, k.TokenHash, k.Prefix,
		k.CreatedAt.UnixNano(), nullableTime(k.LastUsedAt), nullableTime(k.RevokedAt))
	return err
}

func (r *WorkerKeyRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]*domain.WorkerKey, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+workerKeyColumns+" FROM worker_keys WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at DESC",
		userID.String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var keys []*domain.WorkerKey
	for rows.Next() {
		key, err := scanWorkerKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (r *WorkerKeyRepo) GetActiveByHash(ctx context.Context, tokenHash string) (*domain.WorkerKey, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+workerKeyColumns+" FROM worker_keys WHERE token_hash = ? AND revoked_at IS NULL",
		tokenHash)
	key, err := scanWorkerKey(row)
	return key, mapErrNoRows(err, usecase.ErrWorkerKeyNotFound)
}

func (r *WorkerKeyRepo) Revoke(ctx context.Context, id, userID uuid.UUID) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE worker_keys SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL",
		time.Now().UnixNano(), id.String(), userID.String())
	if err != nil {
		return err
	}
	return rowsAffectedOrNotFound(res, usecase.ErrWorkerKeyNotFound)
}

func (r *WorkerKeyRepo) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE worker_keys SET last_used_at = ? WHERE id = ?",
		time.Now().UnixNano(), id.String())
	return err
}

// --- helpers ---------------------------------------------------------------

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixNano()
}

func decodeTime(raw any) time.Time {
	switch v := raw.(type) {
	case int64:
		return time.Unix(0, v).UTC()
	}
	return time.Time{}
}

func mapErrNoRows(err error, notFound error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return notFound
	}
	return err
}

func rowsAffectedOrNotFound(res sql.Result, notFound error) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return notFound
	}
	return nil
}

// isUnique reports whether the error is a SQLite unique-constraint violation.
func isUnique(err error) bool {
	return err != nil && (contains(err.Error(), "UNIQUE constraint failed") ||
		contains(err.Error(), "constraint failed"))
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(haystack) > len(needle) &&
		(indexOf(haystack, needle) >= 0))
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// Open opens (and creates when missing) the userservice database file.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open userservice database: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping userservice database: %w", err)
	}
	return db, nil
}
