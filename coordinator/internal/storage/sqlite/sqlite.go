// Package sqlite implements the usecase repository ports on an embedded
// SQLite database. It is the single-binary storage backend: no external
// service, one file per database, pure-Go driver (modernc.org/sqlite) so the
// static release binaries stay static.
//
// Concurrency model: SQLite allows exactly one writer. Every repository write
// runs inside a TxManager transaction, and the database is opened with a
// busy_timeout, so concurrent writers serialize instead of failing. The
// postgres claim path uses FOR UPDATE SKIP LOCKED; here the same guarantee
// comes from the write lock of the surrounding transaction — ClaimNext is
// always called inside WithinTx by the usecase layer, so SELECT + UPDATE
// cannot interleave.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"

	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// Open opens (and creates when missing) the database file, applying WAL,
// foreign keys, and a busy timeout. Callers own the returned handle.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}
	if err := lockDownDatabase(path); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("lock down sqlite database: %w", err)
	}
	return db, nil
}

// lockDownDatabase restricts the database files to the owner: sqlite creates
// them with the process umask (0644), which would let any local user read job
// metadata and password hashes. WAL/SHM siblings inherit the main file's mode,
// so existing ones are corrected too. Best-effort: failures only warn callers
// via the returned error, never corrupt state.
func lockDownDatabase(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			if err := os.Chmod(candidate, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

// querier is satisfied by both *sql.DB and *sql.Tx, letting every repository
// method run identically inside or outside a transaction.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// txKey is an unexported struct type, so no other package can collide with it
// or reach the transaction we stash in the context.
type txKey struct{}

// TxManager implements usecase.TxManager.
type TxManager struct {
	db *sql.DB
}

func NewTxManager(db *sql.DB) *TxManager {
	return &TxManager{db: db}
}

var _ usecase.TxManager = (*TxManager)(nil)

// WithinTx runs fn inside one transaction, committing on success and rolling
// back on any error or panic. The transaction travels in the context, the
// same pattern as the postgres backend. SQLite write transactions are
// serialized by the database's single-writer lock, so a concurrent writer
// waits on the busy timeout instead of racing.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return fn(ctx)
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	return tx.Commit()
}

// conn returns the transaction bound to ctx, or the database when there is none.
func conn(ctx context.Context, db *sql.DB) querier {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return db
}

// --- JSON and null helpers ------------------------------------------------

// encodeJSON stores a Go value as JSON text, defaulting to "{}".
func encodeJSON(value any) string {
	if value == nil {
		return "{}"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// decodeJSON reads a JSON text column into the destination.
func decodeJSON(raw any, destination any) error {
	text, ok := raw.(string)
	if !ok {
		return nil
	}
	if text == "" {
		return nil
	}
	return json.Unmarshal([]byte(text), destination)
}

// encodeTime stores a time as unix nanoseconds (NULL for zero time).
func encodeTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixNano()
}

// encodeTimePtr stores a nilable time as unix nanoseconds.
func encodeTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixNano()
}

// decodeTime reads a unix-nanosecond column back into a time.Time.
func decodeTime(raw any) time.Time {
	switch v := raw.(type) {
	case int64:
		return time.Unix(0, v).UTC()
	case int:
		return time.Unix(0, int64(v)).UTC()
	}
	return time.Time{}
}

// nullIfEmpty maps "" to SQL NULL.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// mapErrNoRows translates sql.ErrNoRows into the domain not-found errors.
func mapErrNoRows(err error, notFound error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return notFound
	}
	return err
}

var (
	_ usecase.JobRepository        = (*JobRepo)(nil)
	_ usecase.TaskRepository       = (*TaskRepo)(nil)
	_ usecase.ArtifactRepository   = (*ArtifactRepo)(nil)
	_ usecase.WorkerRepository     = (*WorkerRepo)(nil)
	_ usecase.TaskResultRepository = (*TaskResultRepo)(nil)
	_ usecase.UIReadRepository     = (*UIReadRepo)(nil)
	_ usecase.TxManager            = (*TxManager)(nil)
)
