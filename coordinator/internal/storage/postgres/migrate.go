package postgres

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrationNamePattern = regexp.MustCompile(`^([0-9]+)_[a-z0-9_]+\.(up|down)\.sql$`)

// migration is one parsed embedded migration file.
type migration struct {
	version int
	name    string
	sql     string
}

// listMigrations parses and orders the embedded .up.sql files by version.
func listMigrations() ([]migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	up := map[int]migration{}
	for _, entry := range entries {
		match := migrationNamePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		if match[2] != "up" {
			continue
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("migration %q has an invalid version: %w", entry.Name(), err)
		}
		if _, duplicate := up[version]; duplicate {
			return nil, fmt.Errorf("migration version %d is duplicated", version)
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		up[version] = migration{version: version, name: entry.Name(), sql: string(body)}
	}
	if len(up) == 0 {
		return nil, fmt.Errorf("no .up.sql migrations are embedded")
	}
	versions := make([]int, 0, len(up))
	for version := range up {
		versions = append(versions, version)
	}
	sort.Ints(versions)
	migrations := make([]migration, 0, len(versions))
	for _, version := range versions {
		migrations = append(migrations, up[version])
	}
	for index, item := range migrations {
		if item.version != index+1 {
			return nil, fmt.Errorf("embedded migrations are not contiguous: version %d at position %d", item.version, index+1)
		}
	}
	return migrations, nil
}

// Migrate applies every embedded migration that is not yet recorded in the
// schema_migrations table, so the binary provisions its own schema. It is
// idempotent and safe to run concurrently: a PostgreSQL advisory lock
// serializes migrators, and each migration file runs as its own transaction
// (the files carry explicit BEGIN/COMMIT, matching the golang-migrate format
// the CLI and CI still use).
func Migrate(ctx context.Context, databaseURL string, log *slog.Logger) error {
	migrations, err := listMigrations()
	if err != nil {
		return err
	}
	connConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parse database url: %w", err)
	}
	// Migration files contain multiple statements (BEGIN...COMMIT), which the
	// extended query protocol rejects; run them with the simple protocol.
	connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, connConfig)
	if err != nil {
		return fmt.Errorf("connect for migration: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(82473911)"); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock(82473911)") }()

	if _, err := conn.Exec(ctx,
		"CREATE TABLE IF NOT EXISTS schema_migrations (version bigint PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())",
	); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	applied := map[int64]bool{}
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}

	for _, item := range migrations {
		if applied[int64(item.version)] {
			continue
		}
		if log != nil {
			log.Info("applying migration", "version", item.version, "file", item.name)
		}
		if _, err := conn.Exec(ctx, item.sql); err != nil {
			return fmt.Errorf("apply migration %s: %w", item.name, err)
		}
		if _, err := conn.Exec(ctx,
			"INSERT INTO schema_migrations (version) VALUES ($1)", item.version,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", item.name, err)
		}
	}
	return nil
}
