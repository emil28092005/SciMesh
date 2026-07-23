package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *FSStore {
	t.Helper()
	s, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}
	return s
}

func TestPutComputesChecksumAndSize(t *testing.T) {
	s := newStore(t)
	data := bytes.Repeat([]byte("chembl-row\n"), 10000) // ~110 KB, streamed

	sum, size, err := s.Put(context.Background(), "key-1", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	want := sha256.Sum256(data)
	if sum != hex.EncodeToString(want[:]) {
		t.Errorf("sha256 = %s, want %s", sum, hex.EncodeToString(want[:]))
	}
	if size != int64(len(data)) {
		t.Errorf("size = %d, want %d", size, len(data))
	}
}

func TestPutThenOpenRoundTrips(t *testing.T) {
	s := newStore(t)
	data := []byte("partial result csv\n1,2,3\n")

	if _, _, err := s.Put(context.Background(), "key-2", bytes.NewReader(data)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, err := s.Open(context.Background(), "key-2")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()

	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Errorf("round-trip mismatch: got %q", got)
	}
}

func TestPutLeavesNoStagingFileBehind(t *testing.T) {
	s := newStore(t)
	if _, _, err := s.Put(context.Background(), "key-3", strings.NewReader("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	entries, _ := os.ReadDir(s.staging)
	if len(entries) != 0 {
		t.Errorf("staging dir not empty after a successful put: %v", entries)
	}
}

func TestPutFailureLeavesNoArtifactOrStaging(t *testing.T) {
	s := newStore(t)
	// A reader that errors partway through simulates a dropped upload.
	r := io.MultiReader(strings.NewReader("half"), &erroringReader{})

	if _, _, err := s.Put(context.Background(), "key-4", r); err == nil {
		t.Fatal("expected an error from a failing reader")
	}

	if _, err := os.Stat(filepath.Join(s.dir, "key-4")); !os.IsNotExist(err) {
		t.Error("a failed put must not leave a committed artifact")
	}
	if entries, _ := os.ReadDir(s.staging); len(entries) != 0 {
		t.Errorf("a failed put must not leave staging files: %v", entries)
	}
}

func TestPutRejectsUnsafeKeys(t *testing.T) {
	s := newStore(t)
	for _, key := range []string{"", "../escape", "a/b", `a\b`, "with..dots"} {
		if _, _, err := s.Put(context.Background(), key, strings.NewReader("x")); err == nil {
			t.Errorf("key %q should have been rejected", key)
		}
	}
}

func TestPutHonoursContextCancellation(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the copy starts

	if _, _, err := s.Put(ctx, "key-5", strings.NewReader("data")); err == nil {
		t.Fatal("expected cancellation to abort the put")
	}
	if _, err := os.Stat(filepath.Join(s.dir, "key-5")); !os.IsNotExist(err) {
		t.Error("a cancelled put must not leave an artifact")
	}
}

type erroringReader struct{}

func (*erroringReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
