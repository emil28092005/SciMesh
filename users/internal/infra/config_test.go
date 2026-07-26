package infra

import (
	"testing"
	"time"
)

const validSecret = "a-secret-that-is-at-least-32-bytes!!"

// setBaseEnv wires the minimum valid environment. ENV_FILE points at a path that
// does not exist so a developer's stray .env never leaks into the test.
func setBaseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ENV_FILE", "/nonexistent/.env")
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("JWT_SECRET", validSecret)
}

func TestLoadConfigDefaults(t *testing.T) {
	setBaseEnv(t)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr != ":8081" {
		t.Errorf("Addr default = %q, want :8081", cfg.Addr)
	}
	if cfg.TokenTTL != 24*time.Hour {
		t.Errorf("TokenTTL default = %v, want 24h", cfg.TokenTTL)
	}
	if cfg.JWTSecret != validSecret {
		t.Errorf("JWTSecret = %q", cfg.JWTSecret)
	}
}

func TestLoadConfigRequiresDatabaseURL(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("DATABASE_URL", "")

	if _, err := LoadConfig(); err == nil {
		t.Error("expected error when DATABASE_URL is empty")
	}
}

func TestLoadConfigRequiresJWTSecret(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("JWT_SECRET", "")

	if _, err := LoadConfig(); err == nil {
		t.Error("expected error when JWT_SECRET is empty")
	}
}

func TestLoadConfigRejectsShortJWTSecret(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("JWT_SECRET", "too-short")

	if _, err := LoadConfig(); err == nil {
		t.Error("expected error when JWT_SECRET is under 32 bytes")
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("USERSERVICE_ADDR", ":9000")
	t.Setenv("JWT_TTL", "1h")
	t.Setenv("BCRYPT_COST", "6")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr != ":9000" {
		t.Errorf("Addr = %q, want :9000", cfg.Addr)
	}
	if cfg.TokenTTL != time.Hour {
		t.Errorf("TokenTTL = %v, want 1h", cfg.TokenTTL)
	}
	if cfg.BcryptCost != 6 {
		t.Errorf("BcryptCost = %d, want 6", cfg.BcryptCost)
	}
}

func TestLoadConfigRejectsMalformedDuration(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("JWT_TTL", "not-a-duration")

	if _, err := LoadConfig(); err == nil {
		t.Error("expected error for malformed JWT_TTL")
	}
}

func TestLoadConfigRejectsMalformedInt(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("BCRYPT_COST", "abc")

	if _, err := LoadConfig(); err == nil {
		t.Error("expected error for malformed BCRYPT_COST")
	}
}
