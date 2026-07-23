package domain

import "errors"

// Business-rule violations. They live in the innermost layer because they
// describe what the rules are, not how a transport reports them: the HTTP
// adapter maps these to status codes, and nothing here knows 409 exists.
//
// Always compare with errors.Is — outer layers may wrap these with %w.
var (
	ErrJobNotFound      = errors.New("job not found")
	ErrTaskNotFound     = errors.New("task not found")
	ErrWorkerNotFound   = errors.New("worker not found")
	ErrArtifactNotFound = errors.New("artifact not found")
	ErrLeaseConflict    = errors.New("task leased to another worker")
	ErrStaleAttempt     = errors.New("attempt does not match lease")
	ErrResultConflict   = errors.New("different result already recorded")
	ErrInvalidInput     = errors.New("invalid input")
	ErrTaskNotLeased    = errors.New("task is not currently leased")
)
