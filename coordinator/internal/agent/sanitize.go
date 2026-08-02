package agent

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	// Go's regexp (RE2) has no lookbehind, so these patterns conservatively
	// anchor on the characters that typically precede a local path:
	// whitespace, quotes, parens, brackets, or the start of the message.
	windowsPathPattern = regexp.MustCompile(`[A-Za-z]:\\[^\s'"\],)]+`)
	posixPathPattern   = regexp.MustCompile(`(^|[\s'"(\[=])/(?:[^\s'"\],)]+)`)
)

// SanitizeErrorMessage keeps coordinator-visible failures useful without
// exposing local paths. It mirrors the Python worker's sanitizer: local work
// directories and absolute paths are redacted, and the message is truncated
// to 300 characters.
func SanitizeErrorMessage(message string, workDir string) string {
	message = strings.ReplaceAll(message, workDir, "<worker-dir>")
	message = windowsPathPattern.ReplaceAllString(message, "<path>")
	message = posixPathPattern.ReplaceAllString(message, "${1}<path>")
	if len(message) > 300 {
		message = message[:300]
	}
	return message
}

// IsRetryableError classifies failures for the coordinator. Invalid scientific
// input and missing local tools are permanent; everything else (transient
// transport errors, unexpected runner failures) may be retried.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	switch err.(type) {
	case *CoordinatorError:
		return false
	case *os.PathError:
		return false
	}
	return true
}

// TaskRunnerExit classifies subprocess exits.
const (
	ExitPermanent = 3 // runner classified the failure as invalid input
)

func runnerExitError(exit int, stderr string) error {
	if exit == ExitPermanent {
		return &CoordinatorError{msg: stderr}
	}
	return fmt.Errorf("task runner failed with exit code %d: %s", exit, stderr)
}
