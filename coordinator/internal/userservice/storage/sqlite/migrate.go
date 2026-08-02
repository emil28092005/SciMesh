package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrationNamePattern = regexp.MustCompile(`^([0-9]+)_[a-z0-9_]+\.sql$`)

// Migrate applies every embedded migration above the PRAGMA user_version
// watermark, each inside its own transaction.
func Migrate(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	type file struct {
		version int
		name    string
	}
	var files []file
	byVersion := map[int]string{}
	for _, entry := range entries {
		match := migrationNamePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			return fmt.Errorf("migration %q has an invalid version: %w", entry.Name(), err)
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		byVersion[version] = string(body)
		files = append(files, file{version: version, name: entry.Name()})
	}
	if len(files) == 0 {
		return fmt.Errorf("no userservice migrations are embedded")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })

	var applied int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&applied); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for _, item := range files {
		if item.version <= applied {
			continue
		}
		if log != nil {
			log.Info("applying userservice migration", "version", item.version, "file", item.name)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, byVersion[item.version]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", item.name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", item.version)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("advance schema version after %s: %w", item.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", item.name, err)
		}
	}
	return nil
}
