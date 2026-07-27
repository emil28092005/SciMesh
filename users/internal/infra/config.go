// Config: userservice settings, read only from the environment, so the same
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

// defaultEnvFile is loaded by LoadConfig unless ENV_FILE points elsewhere.
const defaultEnvFile = ".env"

type Config struct {
	// HTTP listen address, e.g. ":8081".
	Addr string
	// PostgreSQL connection string (pgx format / libpq URL).
	DatabaseURL string

	// Shared HS256 secret used to sign JWTs. The coordinator verifies tokens
	// with this same secret, so the two values MUST match. This is the only
	// secret shared between the services.
	JWTSecret string
	// How long an issued token stays valid.
	TokenTTL time.Duration
	// bcrypt work factor. 0 falls back to the library default (currently 10).
	BcryptCost int

	// Optional first-admin bootstrap. When both are set and no such account
	// exists, the service creates it with role=admin on startup — the only way
	// to get the first admin, since /register always makes a plain user and
	// promotion needs an existing admin. Idempotent: a no-op once created.
	BootstrapAdminEmail    string
	BootstrapAdminPassword string

	// Minimum log level: debug, info, warn, error.
	LogLevel string
	// Path to a rotated log file. Empty logs to stdout only.
	LogFile string

	// Connection pool upper bound.
	DBMaxConns int32
	// How long to keep retrying the initial database connection at startup
	// before giving up. Covers a Postgres container that is still booting.
	DBConnectTimeout time.Duration
	// Per-request timeout applied to every handler.
	RequestTimeout time.Duration
}

// LoadConfig reads the environment and fails fast on anything required-but-
// missing or malformed, so a misconfigured process never limps along half-wired.
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
		Addr:                   getEnv("USERSERVICE_ADDR", ":8081"),
		DatabaseURL:            os.Getenv("DATABASE_URL"),
		JWTSecret:              os.Getenv("JWT_SECRET"),
		BootstrapAdminEmail:    os.Getenv("BOOTSTRAP_ADMIN_EMAIL"),
		BootstrapAdminPassword: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),
		LogLevel:               getEnv("LOG_LEVEL", "info"),
		LogFile:                os.Getenv("LOG_FILE"),
		TokenTTL:               24 * time.Hour,
		DBMaxConns:             10,
		DBConnectTimeout:       30 * time.Second,
		RequestTimeout:         15 * time.Second,
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return Config{}, fmt.Errorf("JWT_SECRET is required")
	}
	// A short secret makes the HMAC brute-forceable; refuse to start with one.
	if len(cfg.JWTSecret) < 32 {
		return Config{}, fmt.Errorf("JWT_SECRET must be at least 32 bytes")
	}

	var err error
	if cfg.TokenTTL, err = getEnvDuration("JWT_TTL", cfg.TokenTTL); err != nil {
		return Config{}, err
	}
	if cfg.BcryptCost, err = getEnvInt("BCRYPT_COST", cfg.BcryptCost); err != nil {
		return Config{}, err
	}
	if cfg.DBMaxConns, err = getEnvInt32("DB_MAX_CONNS", cfg.DBMaxConns); err != nil {
		return Config{}, err
	}
	if cfg.DBConnectTimeout, err = getEnvDuration("DB_CONNECT_TIMEOUT", cfg.DBConnectTimeout); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeout, err = getEnvDuration("REQUEST_TIMEOUT", cfg.RequestTimeout); err != nil {
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
