package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/emil28092005/SciMesh/users/internal/auth"
	"github.com/emil28092005/SciMesh/users/internal/domain"
)

type ctxKey string

const (
	requestIDKey ctxKey = "request_id"
	userIDKey    ctxKey = "user_id"
	roleKey      ctxKey = "role"
)

// withRequestID stamps every request with an ID for correlated logs and error
// bodies. It wraps the auth middleware rather than the other way round, so even
// a rejected request carries an ID the caller can quote in a bug report.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

func requestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// tokenVerifier is the slice of auth.Issuer the JWT middleware needs. Taking an
// interface keeps the middleware testable with a stub verifier.
type tokenVerifier interface {
	Verify(token string) (*auth.Claims, error)
}

// withJWT verifies the Bearer token and stashes the caller's id and role in the
// request context. It rejects any request without a valid, unexpired HS256
// token — this is what protects endpoints that act on a specific user.
func withJWT(v tokenVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if raw == "" {
				unauthorized(w, r)
				return
			}
			claims, err := v.Verify(raw)
			if err != nil {
				unauthorized(w, r)
				return
			}
			id, err := uuid.Parse(claims.Subject)
			if err != nil {
				unauthorized(w, r)
				return
			}
			ctx := context.WithValue(r.Context(), userIDKey, id)
			ctx = context.WithValue(ctx, roleKey, claims.Role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func unauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeJSON(w, http.StatusUnauthorized, errorResponse{
		Error:     "unauthorized",
		RequestID: requestIDFrom(r.Context()),
	})
}

// userIDFrom returns the authenticated caller's id, set by withJWT.
func userIDFrom(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDKey).(uuid.UUID)
	return id, ok
}

// withAdmin rejects any caller whose token role is not admin. It must sit inside
// withJWT, which stamps the role after verifying the token.
func withAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if role, ok := r.Context().Value(roleKey).(domain.Role); !ok || role != domain.RoleAdmin {
			writeJSON(w, http.StatusForbidden, errorResponse{
				Error:     "admin role required",
				RequestID: requestIDFrom(r.Context()),
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// withAccessLog records one structured line per request — the minimum needed to
// debug a distributed system after the fact.
func withAccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			log.Info("request",
				"request_id", requestIDFrom(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// chain applies middleware so that the first argument is the outermost layer.
func chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}
