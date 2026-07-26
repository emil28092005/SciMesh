package http

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/authctx"
	tokenpkg "github.com/emil28092005/SciMesh/coordinator/internal/token"
)

type ctxKey string

const requestIDKey ctxKey = "request_id"

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

// withAuth enforces the shared bearer token every worker presents.
// An empty token disables the check (local development only).
// withAuth authenticates a request one of two ways. Workers (and legacy
// submitters) present the shared service token. When user-JWT auth is enabled
// (verifier != nil), a submitter may instead present a userservice JWT; on
// success the requester is stamped into the context so the job use cases can
// record owner_id and enforce ownership. An empty token with no verifier
// disables auth entirely (dev only).
func withAuth(token string, verifier *tokenpkg.Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" && verifier == nil {
				next.ServeHTTP(w, r)
				return
			}
			presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

			// Shared service token: constant-time compare so a byte-by-byte
			// early exit cannot leak the token through response timing.
			if token != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1 {
				next.ServeHTTP(w, r)
				return
			}

			// Otherwise try a user JWT, if that path is configured.
			if verifier != nil && presented != "" {
				if claims, err := verifier.Verify(presented); err == nil {
					ctx := authctx.With(r.Context(), authctx.Requester{
						UserID:   claims.UserID,
						Role:     claims.Role,
						Verified: claims.Verified,
					})
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, errorResponse{
				Error:     "unauthorized",
				RequestID: requestIDFrom(r.Context()),
			})
		})
	}
}

// withBasicAuth protects the local operator UI with a credential distinct from
// the worker bearer token. The username is intentionally ignored; the password
// is the configured UI token. Basic Auth is suitable only for localhost or a
// TLS-terminating trusted reverse proxy.
func withBasicAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, password, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(password), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="SciMesh UI", charset="UTF-8"`)
				writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "unauthorized", RequestID: requestIDFrom(r.Context())})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// withSameOrigin rejects browser form/fetch writes initiated by another origin.
// A missing Origin is allowed for direct local tools; authenticated UI pages use
// the browser-supplied Origin header on state-changing requests.
func withSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			if origin != scheme+"://"+r.Host {
				writeJSON(w, http.StatusForbidden, errorResponse{Error: "cross-origin request rejected", RequestID: requestIDFrom(r.Context())})
				return
			}
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
