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
	Token string
	// Local operator UI credential. Empty disables the embedded UI entirely.
	UIToken string

	// Minimum log level: debug, info, warn, error.
	LogLevel string
	// Path to a rotated log file. Empty logs to stdout only.
	LogFile string
	// Directory where artifact bytes are stored.
	StorageDir string
	// Upper bound on an uploaded dataset or artifact body, in bytes.
	MaxUploadBytes int64

	// Connection pool upper bound.
	DBMaxConns int32
	// How long to keep retrying the initial database connection at startup
	// before giving up. Covers a Postgres container that is still booting.
	DBConnectTimeout time.Duration
	// Per-request context timeout applied to handlers and DB calls.
	RequestTimeout time.Duration

	// Suggested heartbeat cadence returned to workers on registration.
	HeartbeatInterval time.Duration
	// Default lease length handed out on claim.
	LeaseDuration time.Duration
	// Default attempt ceiling for newly created tasks.
	DefaultMaxAttempts int
	// How often the background lease-reaper runs.
	ReaperInterval time.Duration
	// A worker silent for longer than this is marked offline by the reaper.
	WorkerOfflineAfter time.Duration
}

// Load reads the environment and fails fast on anything required-but-missing
// or malformed, so a misconfigured process never limps along half-wired.
//
// A .env file (path overridable via ENV_FILE) is loaded first as a local-dev
// convenience. It only fills variables the environment does not already define.
func LoadConfig() (Config, error) {
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
		Addr:        getEnv("COORDINATOR_ADDR", ":8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		// COORDINATOR_TOKEN is the contract name; WORKER_AUTH_TOKEN is the
		// former name, still honoured so existing .env files keep working.
		Token:              getEnv("COORDINATOR_TOKEN", os.Getenv("WORKER_AUTH_TOKEN")),
		UIToken:            os.Getenv("UI_AUTH_TOKEN"),
		LogLevel:           getEnv("LOG_LEVEL", "info"),
		LogFile:            os.Getenv("LOG_FILE"),
		StorageDir:         getEnv("COORDINATOR_STORAGE_DIR", "./data"),
		MaxUploadBytes:     1 << 30, // 1 GiB
		DBMaxConns:         10,
		DBConnectTimeout:   30 * time.Second,
		RequestTimeout:     15 * time.Second,
		HeartbeatInterval:  15 * time.Second,
		LeaseDuration:      2 * time.Minute,
		DefaultMaxAttempts: 3,
		ReaperInterval:     30 * time.Second,
		WorkerOfflineAfter: 1 * time.Minute,
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.UIToken != "" && cfg.Token != "" && cfg.UIToken == cfg.Token {
		return Config{}, fmt.Errorf("UI_AUTH_TOKEN must differ from the worker auth token")
	}

	var err error
	if cfg.DBMaxConns, err = getEnvInt32("DB_MAX_CONNS", cfg.DBMaxConns); err != nil {
		return Config{}, err
	}
	if cfg.DBConnectTimeout, err = getEnvDuration("DB_CONNECT_TIMEOUT", cfg.DBConnectTimeout); err != nil {
		return Config{}, err
	}
	if cfg.MaxUploadBytes, err = getEnvInt64("MAX_UPLOAD_BYTES", cfg.MaxUploadBytes); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeout, err = getEnvDuration("REQUEST_TIMEOUT", cfg.RequestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.HeartbeatInterval, err = getEnvDuration("HEARTBEAT_INTERVAL", cfg.HeartbeatInterval); err != nil {
		return Config{}, err
	}
	if cfg.LeaseDuration, err = getEnvDuration("LEASE_DURATION", cfg.LeaseDuration); err != nil {
		return Config{}, err
	}
	if cfg.ReaperInterval, err = getEnvDuration("REAPER_INTERVAL", cfg.ReaperInterval); err != nil {
		return Config{}, err
	}
	if cfg.WorkerOfflineAfter, err = getEnvDuration("WORKER_OFFLINE_AFTER", cfg.WorkerOfflineAfter); err != nil {
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

func getEnvInt64(key string, def int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
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
