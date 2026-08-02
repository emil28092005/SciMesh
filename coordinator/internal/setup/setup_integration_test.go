//go:build integration

package setup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestRunProvisionsDatabaseSchemaAndEnvFile(t *testing.T) {
	ctx := context.Background()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	// The wizard must create a *missing* database through the admin URL.
	slash := strings.LastIndex(base, "/")
	target := base[:slash+1] + "scimesh_setup_test"
	admin := base[:slash+1] + "postgres"

	cleanup := func() {
		conn, err := pgx.Connect(ctx, admin)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		_, _ = conn.Exec(ctx, `DROP DATABASE IF EXISTS "scimesh_setup_test"`)
	}
	cleanup()
	t.Cleanup(cleanup)

	envPath := filepath.Join(t.TempDir(), ".env")
	var output strings.Builder
	options := Options{
		DatabaseURL:      target,
		AdminDatabaseURL: admin,
		EnvFile:          envPath,
		Force:            true,
		Yes:              true,
		ConnectTimeout:   10 * time.Second,
		Out:              &output,
		In:               strings.NewReader(""),
	}
	summary, err := Run(ctx, options)
	if err != nil {
		t.Fatalf("setup run: %v", err)
	}
	for _, expected := range []string{"created database", "schema migrations applied"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("progress output is missing %q:\n%s", expected, output.String())
		}
	}
	for _, expected := range []string{"Setup complete", "Start the coordinator"} {
		if !strings.Contains(summary, expected) {
			t.Errorf("summary is missing %q:\n%s", expected, summary)
		}
	}

	// The database now exists, is migrated, and the env file is written.
	conn, err := pgx.Connect(ctx, target)
	if err != nil {
		t.Fatalf("connect to provisioned database: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var watermark int64
	if err := conn.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&watermark); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if watermark < 13 {
		t.Errorf("schema watermark = %d, want >= 13", watermark)
	}
	envContent, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	env := string(envContent)
	if !strings.Contains(env, "DATABASE_URL="+target) || !strings.Contains(env, "JWT_SECRET=") {
		t.Errorf("env file is incomplete:\n%s", env)
	}

	// A second run is idempotent: no create-database error, same outcome.
	if _, err := Run(ctx, options); err != nil {
		t.Fatalf("second setup run: %v", err)
	}
}
