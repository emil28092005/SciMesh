// Package blob stores artifact bytes on the local filesystem. It implements
// usecase.BlobStore; no other layer knows where or how the bytes are kept.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

// FSStore keeps each artifact as one file under dir, named by its storage key.
type FSStore struct {
	dir     string
	staging string
}

var _ usecase.BlobStore = (*FSStore)(nil)

// NewFSStore prepares the storage and staging directories. Staging lives inside
// dir so a finished file can be renamed into place on the same filesystem —
// rename is only atomic within one filesystem.
func NewFSStore(dir string) (*FSStore, error) {
	staging := filepath.Join(dir, ".staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return nil, fmt.Errorf("create blob dirs: %w", err)
	}
	return &FSStore{dir: dir, staging: staging}, nil
}

// Put streams r to a staging file while hashing it, then atomically renames it
// into place. A caller that dies mid-upload leaves at most a staging temp file,
// never a half-written artifact that looks complete.
func (s *FSStore) Put(ctx context.Context, key string, r io.Reader) (string, int64, error) {
	if err := checkKey(key); err != nil {
		return "", 0, err
	}

	tmp, err := os.CreateTemp(s.staging, key+"-*")
	if err != nil {
		return "", 0, fmt.Errorf("create staging file: %w", err)
	}
	tmpName := tmp.Name()
	// On any failure past this point, do not leave the temp file behind.
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	// Tee the stream: one copy to disk, one to the hasher, in a single pass so
	// the bytes are never held in memory or read twice.
	size, err := io.Copy(io.MultiWriter(tmp, h), &ctxReader{ctx: ctx, r: r})
	if err != nil {
		_ = tmp.Close()
		return "", 0, fmt.Errorf("write artifact: %w", err)
	}
	// fsync before rename so a crash cannot leave a renamed-but-empty file.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", 0, fmt.Errorf("sync artifact: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("close artifact: %w", err)
	}

	final := filepath.Join(s.dir, key)
	if err := os.Rename(tmpName, final); err != nil {
		return "", 0, fmt.Errorf("commit artifact: %w", err)
	}
	tmpName = "" // committed — the deferred cleanup must not delete it now

	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// Open returns the artifact bytes for streaming to a client. The caller closes.
func (s *FSStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.dir, key))
	if err != nil {
		return nil, err
	}
	return f, nil
}

// checkKey rejects anything that could escape the storage directory. Keys are
// coordinator-generated UUIDs, so this is defence in depth, not the only guard.
func checkKey(key string) error {
	if key == "" || strings.ContainsAny(key, `/\`) || strings.Contains(key, "..") {
		return fmt.Errorf("invalid storage key %q", key)
	}
	return nil
}

// ctxReader aborts a copy when the request context is cancelled, so a stalled
// or disconnected upload does not tie up a file handle indefinitely.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
