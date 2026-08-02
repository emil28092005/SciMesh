package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateSecretIsRandomAndStrong(t *testing.T) {
	first, err := generateSecret()
	if err != nil {
		t.Fatal(err)
	}
	second, err := generateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 || len(second) != 64 {
		t.Fatalf("secrets must be 32 random bytes as hex, got %d and %d", len(first), len(second))
	}
	if first == second {
		t.Fatal("two generated secrets must differ")
	}
}

func TestWriteEnvFileContentsAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := writeEnvFile(Options{
		EnvFile:     path,
		DatabaseURL: "postgres://scimesh@localhost/scimesh",
	}, "s3cr3t", "./data"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "DATABASE_URL=postgres://scimesh@localhost/scimesh\nJWT_SECRET=s3cr3t\nCOORDINATOR_STORAGE_DIR=./data\n"
	if got := string(content); got != want {
		t.Fatalf("env file = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("env file mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestWriteEnvFileRefusesWithoutForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := writeEnvFile(Options{EnvFile: path, DatabaseURL: "postgres://x@localhost/a"}, "a", "./data"); err != nil {
		t.Fatal(err)
	}
	if err := writeEnvFile(Options{EnvFile: path, DatabaseURL: "postgres://x@localhost/a"}, "b", "./data"); err == nil {
		t.Fatal("second write without --force must fail")
	}
	if err := writeEnvFile(Options{EnvFile: path, Force: true, DatabaseURL: "postgres://x@localhost/a"}, "b", "./data"); err != nil {
		t.Fatalf("write with --force: %v", err)
	}
}

func TestPromptReadsAnswer(t *testing.T) {
	var out bytes.Buffer
	answer := prompt(Options{Out: &out, In: strings.NewReader("postgres://custom\n")}, "Database URL", "default")
	if answer != "postgres://custom" {
		t.Fatalf("answer = %q, want the typed value", answer)
	}
	if !strings.Contains(out.String(), "Database URL [default]:") {
		t.Fatalf("prompt output = %q", out.String())
	}
	fallback := prompt(Options{Out: &out, In: strings.NewReader("\n")}, "Question", "fb")
	if fallback != "fb" {
		t.Fatalf("empty answer must fall back, got %q", fallback)
	}
}

func TestSanitizeDatabaseURL(t *testing.T) {
	cases := map[string]string{
		"postgres://scimesh:hunter2@localhost:5432/scimesh?sslmode=disable": "postgres://scimesh:***@localhost:5432/scimesh?sslmode=disable",
		"postgresql://scimesh@localhost/scimesh":                            "postgresql://scimesh@localhost/scimesh",
		"not-a-url":                                                         "not-a-url",
	}
	for raw, want := range cases {
		got := SanitizeDatabaseURL(raw)
		if got != want {
			t.Errorf("sanitize(%q) = %q, want %q", raw, got, want)
		}
		if strings.Contains(got, "hunter2") {
			t.Errorf("sanitize(%q) leaked the password", raw)
		}
	}
}
