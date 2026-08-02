// Package setup implements the `coordinator setup` wizard: database reachability
// and creation, embedded schema migration, secret generation, and .env writing.
// The wizard never logs or echoes secrets.
package setup

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emil28092005/SciMesh/coordinator/internal/storage/postgres"
)

// Options configures one wizard run.
type Options struct {
	// DatabaseURL is the target coordinator database (pgx/libpq URL).
	DatabaseURL string
	// AdminDatabaseURL, when set, is used to create a missing target database.
	// Defaults to the target URL with the database name replaced by "postgres".
	AdminDatabaseURL string
	// EnvFile is where the generated settings are written (default ".env").
	EnvFile string
	// Force overwrites an existing EnvFile.
	Force bool
	// Yes disables interactive prompts; missing values fail instead.
	Yes bool
	// ConnectTimeout bounds the reachability check.
	ConnectTimeout time.Duration
	// Out receives progress and summary output; In feeds interactive answers.
	Out io.Writer
	In  io.Reader
}

// Run executes the wizard and returns a summary of what was done.
func Run(ctx context.Context, options Options) (string, error) {
	if options.DatabaseURL == "" {
		return "", fmt.Errorf("DATABASE_URL is required (or pass --db)")
	}
	if options.EnvFile == "" {
		options.EnvFile = ".env"
	}
	if options.ConnectTimeout <= 0 {
		options.ConnectTimeout = 5 * time.Second
	}
	if options.Out == nil {
		options.Out = os.Stdout
	}

	report := func(format string, args ...any) {
		_, _ = fmt.Fprintf(options.Out, format+"\n", args...)
	}

	report("SciMesh coordinator setup")
	report("")

	// 1. Reachability, with optional database creation.
	target, err := pgx.ParseConfig(options.DatabaseURL)
	if err != nil {
		return "", fmt.Errorf("DATABASE_URL is not a valid postgres URL: %w", err)
	}
	if err := probeDatabase(ctx, target, options.ConnectTimeout); err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "3D000" {
			return "", fmt.Errorf("cannot reach the coordinator database: %w", err)
		}
		report("database %q does not exist yet", target.Database)
		admin, err := resolveAdminConfig(options, target)
		if err != nil {
			return "", err
		}
		if err := createDatabase(ctx, admin, target.Database, options.ConnectTimeout); err != nil {
			return "", fmt.Errorf("cannot create database %q: %w", target.Database, err)
		}
		report("created database %q", target.Database)
	}
	report("database %q is reachable", target.Database)

	// 2. Apply the embedded schema migrations (idempotent).
	if err := postgres.Migrate(ctx, options.DatabaseURL, nil); err != nil {
		return "", fmt.Errorf("apply schema migrations: %w", err)
	}
	report("schema migrations applied")

	// 3. JWT secret: reuse the environment value when strong, else generate.
	secret := os.Getenv("JWT_SECRET")
	if secret != "" && len(secret) < 32 {
		return "", fmt.Errorf("JWT_SECRET must be at least 32 bytes")
	}
	if secret == "" {
		generated, err := generateSecret()
		if err != nil {
			return "", fmt.Errorf("generate JWT_SECRET: %w", err)
		}
		secret = generated
		report("generated a fresh JWT_SECRET")
	}

	// 4. Write the .env file.
	storageDir := os.Getenv("COORDINATOR_STORAGE_DIR")
	if storageDir == "" {
		storageDir = "./data"
	}
	if err := writeEnvFile(options, secret, storageDir); err != nil {
		return "", err
	}

	// 5. Summary.
	var summary strings.Builder
	fmt.Fprintf(&summary, "Setup complete.\n\n")
	fmt.Fprintf(&summary, "Ready:\n")
	fmt.Fprintf(&summary, "  - database %s is reachable and migrated\n", target.Database)
	fmt.Fprintf(&summary, "  - settings written to %s (chmod 0600)\n", options.EnvFile)
	fmt.Fprintf(&summary, "\nStart the coordinator:\n")
	fmt.Fprintf(&summary, "  ENV_FILE=%s ./coordinator\n", options.EnvFile)
	fmt.Fprintf(&summary, "\nOptional — userservice for UI logins (must share JWT_SECRET):\n")
	fmt.Fprintf(&summary, "  cd users && JWT_SECRET=%q docker compose up -d\n", secret)
	fmt.Fprintf(&summary, "  then set USERSERVICE_URL=http://localhost:8081 and BOOTSTRAP_ADMIN_EMAIL/PASSWORD\n")
	fmt.Fprintf(&summary, "\nThe wizard cannot run PostgreSQL or the userservice for you; the\n")
	fmt.Fprintf(&summary, "commands above are the supported way to start them.\n")
	return summary.String(), nil
}

// probeDatabase verifies the target database accepts connections.
func probeDatabase(ctx context.Context, config *pgx.ConnConfig, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return err
	}
	return conn.Close(ctx)
}

// resolveAdminConfig picks the maintenance connection used to create
// databases. pgx's ConnConfig.ConnString() caches the original URL, so the
// config itself (not a re-rendered string) is what the caller connects with.
func resolveAdminConfig(options Options, target *pgx.ConnConfig) (*pgx.ConnConfig, error) {
	if options.AdminDatabaseURL != "" {
		config, err := pgx.ParseConfig(options.AdminDatabaseURL)
		if err != nil {
			return nil, fmt.Errorf("--admin-db is not a valid postgres URL: %w", err)
		}
		return config, nil
	}
	admin := *target
	admin.Database = "postgres"
	return &admin, nil
}

// createDatabase creates the named database through the maintenance connection.
func createDatabase(ctx context.Context, admin *pgx.ConnConfig, name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, admin)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		return err
	}
	return nil
}

// generateSecret returns 32 random bytes as lowercase hex.
func generateSecret() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// writeEnvFile writes the settings, refusing to clobber without --force.
func writeEnvFile(options Options, secret, storageDir string) error {
	path := filepath.Clean(options.EnvFile)
	if _, err := os.Stat(path); err == nil && !options.Force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", path)
	}
	content := strings.Join([]string{
		"DATABASE_URL=" + options.DatabaseURL,
		"JWT_SECRET=" + secret,
		"COORDINATOR_STORAGE_DIR=" + storageDir,
		"", // trailing newline
	}, "\n")
	// #nosec G703 -- the env file path is operator-supplied (--env-file / ENV_FILE).
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// SanitizeDatabaseURL hides the password for logging.
func SanitizeDatabaseURL(raw string) string {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	start := 0
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		start = len("postgres://")
	}
	colon := strings.Index(raw[start:at], ":")
	if colon < 0 {
		return raw
	}
	colon += start
	return raw[:colon] + ":***@" + raw[at+1:]
}

// prompt asks a question and returns the trimmed answer ("" on EOF).
func prompt(options Options, question, fallback string) string {
	_, _ = fmt.Fprintf(options.Out, "%s [%s]: ", question, fallback)
	reader := bufio.NewReader(options.In)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fallback
	}
	answer := strings.TrimSpace(line)
	if answer == "" {
		return fallback
	}
	return answer
}
