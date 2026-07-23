package infra

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRejectsSharedUIAndWorkerToken(t *testing.T) {
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "missing.env"))
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("COORDINATOR_TOKEN", "shared-secret")
	t.Setenv("UI_AUTH_TOKEN", "shared-secret")

	_, err := LoadConfig()
	if err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("LoadConfig error = %v, want distinct-token error", err)
	}
}

func TestLoadConfigAllowsDistinctUIAndWorkerTokens(t *testing.T) {
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "missing.env"))
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("COORDINATOR_TOKEN", "worker-secret")
	t.Setenv("UI_AUTH_TOKEN", "ui-secret")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Token != "worker-secret" || cfg.UIToken != "ui-secret" {
		t.Fatalf("unexpected tokens: %+v", cfg)
	}
}
