package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// WorkloadSettingsRepo persists the per-workload enable/disable overrides.
// Absence of a row means the workload is enabled (the catalog default).
type WorkloadSettingsRepo struct{ db *sql.DB }

func NewWorkloadSettingsRepo(db *sql.DB) *WorkloadSettingsRepo { return &WorkloadSettingsRepo{db: db} }

var _ usecase.WorkloadSettingsRepository = (*WorkloadSettingsRepo)(nil)

func (r *WorkloadSettingsRepo) GetEnabled(ctx context.Context, workload string) (bool, error) {
	var enabled int
	err := conn(ctx, r.db).QueryRowContext(ctx,
		"SELECT enabled FROM workload_settings WHERE workload = ?", workload).Scan(&enabled)
	if err == sql.ErrNoRows {
		return true, nil // no override: catalog default enabled
	}
	if err != nil {
		return false, fmt.Errorf("get workload setting: %w", err)
	}
	return enabled == 1, nil
}

func (r *WorkloadSettingsRepo) List(ctx context.Context) ([]usecase.WorkloadSetting, error) {
	rows, err := conn(ctx, r.db).QueryContext(ctx,
		"SELECT workload, enabled, updated_at FROM workload_settings ORDER BY workload ASC")
	if err != nil {
		return nil, fmt.Errorf("list workload settings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []usecase.WorkloadSetting
	for rows.Next() {
		var (
			name      string
			enabled   int
			updatedAt int64
		)
		if err := rows.Scan(&name, &enabled, &updatedAt); err != nil {
			return nil, err
		}
		out = append(out, usecase.WorkloadSetting{
			Workload:  name,
			Enabled:   enabled == 1,
			UpdatedAt: decodeTime(updatedAt),
		})
	}
	return out, rows.Err()
}

func (r *WorkloadSettingsRepo) SetEnabled(ctx context.Context, workload string, enabled bool, now time.Time) error {
	_, err := conn(ctx, r.db).ExecContext(ctx, `
INSERT INTO workload_settings (workload, enabled, updated_at) VALUES (?, ?, ?)
ON CONFLICT (workload) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
		workload, boolInt(enabled), now.UnixNano())
	if err != nil {
		return fmt.Errorf("set workload setting: %w", err)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
