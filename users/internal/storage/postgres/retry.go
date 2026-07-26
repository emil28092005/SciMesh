package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/jackc/pgx/v5/pgconn"
)

// Transient PostgreSQL failures. Under concurrent claiming these are expected
// rather than exceptional: two coordinators touching neighbouring rows can
// deadlock or fail to serialize, and the correct response is to try again.
const (
	codeSerializationFailure = "40001"
	codeDeadlockDetected     = "40P01"
	codeTooManyConnections   = "53300"
	codeCannotConnectNow     = "57P03"
)

// Retry budget: short and bounded. A worker polling for tasks would rather get
// a fast error and poll again than have its request hang for half a minute.
const (
	retryInitialInterval = 50 * time.Millisecond
	retryMaxInterval     = 1 * time.Second
	retryMaxElapsedTime  = 5 * time.Second
)

// isTransient reports whether err is worth retrying.
//
// The default is *not* to retry: a constraint violation or a syntax error will
// fail identically every time, and retrying it only multiplies the damage.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	// A cancelled caller does not want another attempt.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case codeSerializationFailure, codeDeadlockDetected,
			codeTooManyConnections, codeCannotConnectNow:
			return true
		default:
			return false
		}
	}

	// Connection-level trouble (dropped socket, closed pool). pgconn knows
	// whether the query could have been executed before the failure — retrying
	// a maybe-executed write would risk duplicating it.
	return pgconn.SafeToRetry(err)
}

// withRetry runs op, retrying only transient database failures with
// exponential backoff and jitter, and giving up as soon as ctx is done.
//
// Jitter matters here: without it, several coordinators that collide once will
// retry in lockstep and collide again at exactly the same moment.
func withRetry(ctx context.Context, op func(context.Context) error) error {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = retryInitialInterval
	b.MaxInterval = retryMaxInterval
	b.MaxElapsedTime = retryMaxElapsedTime
	// RandomizationFactor defaults to 0.5, which is the jitter.

	return backoff.Retry(func() error {
		err := op(ctx)
		if err == nil {
			return nil
		}
		if !isTransient(err) {
			return backoff.Permanent(err) // stop now, do not burn the budget
		}
		return err
	}, backoff.WithContext(b, ctx))
}
