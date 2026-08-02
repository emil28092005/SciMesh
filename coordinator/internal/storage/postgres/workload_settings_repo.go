package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// WorkloadSettingsRepo persists the per-workload enable/disable overrides.
// Absence of a row means the workload is enabled (the catalog default).
type WorkloadSettingsRepo struct{ pool *pgxpool.Pool }

func NewWorkloadSettingsRepo(pool *pgxpool.Pool) *WorkloadSettingsRepo {
	return &WorkloadSettingsRepo{pool: pool}
}

var _ usecase.WorkloadSettingsRepository = (*WorkloadSettingsRepo)(nil)

func (r *WorkloadSettingsRepo) GetEnabled(ctx context.Context, workload string) (bool, error) {
	var enabled bool
	err := conn(ctx, r.pool).QueryRow(ctx,
		"SELECT enabled FROM workload_settings WHERE workload = $1", workload).Scan(&enabled)
	if err != nil && err.Error() == "no rows in result set" {
		return true, nil // no override: catalog default enabled
	}
	if err != nil {
		return false, fmt.Errorf("get workload setting: %w", err)
	}
	return enabled, nil
}

func (r *WorkloadSettingsRepo) List(ctx context.Context) ([]usecase.WorkloadSetting, error) {
	rows, err := conn(ctx, r.pool).Query(ctx,
		"SELECT workload, enabled, updated_at FROM workload_settings ORDER BY workload ASC")
	if err != nil {
		return nil, fmt.Errorf("list workload settings: %w", err)
	}
	defer rows.Close()
	var out []usecase.WorkloadSetting
	for rows.Next() {
		var s usecase.WorkloadSetting
		if err := rows.Scan(&s.Workload, &s.Enabled, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *WorkloadSettingsRepo) SetEnabled(ctx context.Context, workload string, enabled bool, now time.Time) error {
	sql, args, err := psql.Insert("workload_settings").
		Columns("workload", "enabled", "updated_at").
		Values(workload, enabled, now).
		Suffix(`ON CONFLICT (workload) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = EXCLUDED.updated_at`).
		ToSql()
	if err != nil {
		return err
	}
	if _, err := conn(ctx, r.pool).Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("set workload setting: %w", err)
	}
	return nil
}
