// Config: coordinator settings, read only from the environment, so the same
// binary behaves identically in CI, local, and prod.
package infra

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// defaultEnvFile is loaded by Load unless ENV_FILE points elsewhere.
const defaultEnvFile = ".env"

type Config struct {
	// HTTP listen address, e.g. ":8080".
	Addr string
	// PostgreSQL connection string (pgx format / libpq URL).
	DatabaseURL string
	// Shared bearer token workers must present. Empty disables auth (dev only).
	WorkerAuthToken string

	// Connection pool upper bound.
	DBMaxConns int32
	// Per-request context timeout applied to handlers and DB calls.
	RequestTimeout time.Duration

	// Default lease length handed out on claim.
	LeaseDuration time.Duration
	// Default attempt ceiling for newly created tasks.
	DefaultMaxAttempts int
	// How often the background lease-reaper runs.
	ReaperInterval time.Duration
}

// Load reads the environment and fails fast on anything required-but-missing
// or malformed, so a misconfigured process never limps along half-wired.
//
// A .env file (path overridable via ENV_FILE) is loaded first as a local-dev
// convenience. It only fills variables the environment does not already define.
func Load() (Config, error) {
	envFile := os.Getenv("ENV_FILE")
	if envFile == "" {
		envFile = defaultEnvFile
	}
	// godotenv.Load never overwrites variables already present in the
	// environment, so an orchestrator's values always beat the file. A missing
	// file is expected in production, where env vars are injected directly.
	if err := godotenv.Load(envFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Config{}, fmt.Errorf("load env file %q: %w", envFile, err)
	}

	cfg := Config{
		Addr:               getEnv("COORDINATOR_ADDR", ":8080"),
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		WorkerAuthToken:    os.Getenv("WORKER_AUTH_TOKEN"),
		DBMaxConns:         10,
		RequestTimeout:     15 * time.Second,
		LeaseDuration:      2 * time.Minute,
		DefaultMaxAttempts: 3,
		ReaperInterval:     30 * time.Second,
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	var err error
	if cfg.DBMaxConns, err = getEnvInt32("DB_MAX_CONNS", cfg.DBMaxConns); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeout, err = getEnvDuration("REQUEST_TIMEOUT", cfg.RequestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.LeaseDuration, err = getEnvDuration("LEASE_DURATION", cfg.LeaseDuration); err != nil {
		return Config{}, err
	}
	if cfg.ReaperInterval, err = getEnvDuration("REAPER_INTERVAL", cfg.ReaperInterval); err != nil {
		return Config{}, err
	}
	if cfg.DefaultMaxAttempts, err = getEnvInt("DEFAULT_MAX_ATTEMPTS", cfg.DefaultMaxAttempts); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func getEnvInt32(key string, def int32) (int32, error) {
	n, err := getEnvInt(key, int(def))
	if err != nil {
		return 0, err
	}
	// On 64-bit builds int is wider than int32, so an oversized value would
	// wrap silently — DB_MAX_CONNS=2147483648 becoming a negative pool size.
	if n < math.MinInt32 || n > math.MaxInt32 {
		return 0, fmt.Errorf("%s: %d is out of range for int32", key, n)
	}
	return int32(n), nil
}

func getEnvDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}
