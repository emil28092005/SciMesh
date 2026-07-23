package infra

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// NewLogger builds the process logger.
//
// It always writes JSON to stdout, so `docker logs` and any 12-factor log
// collector keep working. When LogFile is set it *also* writes to a
// size-rotated file, so logs survive a container rebuild instead of vanishing
// with the previous stdout stream. Rotation is delegated to lumberjack rather
// than hand-rolled.
//
// The returned Closer flushes and closes the file; call it on shutdown.
func NewLogger(cfg Config) (*slog.Logger, io.Closer, error) {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}

	var (
		out    io.Writer = os.Stdout
		closer io.Closer = noopCloser{}
	)

	if cfg.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0o755); err != nil {
			return nil, nil, fmt.Errorf("create log directory: %w", err)
		}
		rotator := &lumberjack.Logger{
			Filename:   cfg.LogFile,
			MaxSize:    50, // megabytes before a rotation
			MaxBackups: 5,  // keep this many rotated files
			MaxAge:     30, // days
			Compress:   true,
		}
		// Tee to both: the console stays live while the file is the durable copy.
		out = io.MultiWriter(os.Stdout, rotator)
		closer = rotator
	}

	return slog.New(slog.NewJSONHandler(out, opts)), closer, nil
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }
