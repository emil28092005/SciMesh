package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsTransient(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"serialization failure", &pgconn.PgError{Code: codeSerializationFailure}, true},
		{"deadlock", &pgconn.PgError{Code: codeDeadlockDetected}, true},
		{"too many connections", &pgconn.PgError{Code: codeTooManyConnections}, true},
		// A unique-violation repeats identically forever — retrying is pointless.
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"syntax error", &pgconn.PgError{Code: "42601"}, false},
		{"context cancelled", context.Canceled, false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"unknown error", errors.New("boom"), false},
		// Wrapping must not hide the cause: errors.As walks the chain.
		{"wrapped deadlock", errors2Wrap(&pgconn.PgError{Code: codeDeadlockDetected}), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransient(tt.err); got != tt.want {
				t.Errorf("isTransient(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func errors2Wrap(err error) error {
	return errors.Join(errors.New("query failed"), err)
}

func TestWithRetrySucceedsAfterTransientFailures(t *testing.T) {
	calls := 0
	err := withRetry(context.Background(), func(context.Context) error {
		calls++
		if calls < 3 {
			return &pgconn.PgError{Code: codeSerializationFailure}
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestWithRetryStopsOnPermanentError(t *testing.T) {
	permanent := &pgconn.PgError{Code: "23505"} // unique violation
	calls := 0

	err := withRetry(context.Background(), func(context.Context) error {
		calls++
		return permanent
	})

	if !errors.Is(err, permanent) {
		t.Errorf("err = %v, want the original error", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 — a permanent error must not be retried", calls)
	}
}

func TestWithRetryHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	calls := 0
	start := time.Now()
	err := withRetry(ctx, func(context.Context) error {
		calls++
		return &pgconn.PgError{Code: codeDeadlockDetected}
	})

	if err == nil {
		t.Fatal("expected an error once the context expired")
	}
	// Must abort at the deadline, not run the full 5s retry budget.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v, expected to stop at the context deadline", elapsed)
	}
}
