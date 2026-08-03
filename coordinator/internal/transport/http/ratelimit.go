package http

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// documentedLimits describes the default policy for the two public surfaces;
// keep in sync with loginRatePerMinute and exchangeRatePerMinute below.
const (
	loginRatePerMinute    = 10
	loginBurst            = 5
	exchangeRatePerMinute = 30
	exchangeBurst         = 10
)

// tokenBucket is a fixed-rate token bucket for one client address.
type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	rate   float64 // tokens per second
	burst  float64
}

func newTokenBucket(ratePerMinute, burst float64) *tokenBucket {
	return &tokenBucket{
		tokens: burst,
		last:   time.Now(),
		rate:   ratePerMinute / 60,
		burst:  burst,
	}
}

// allow consumes one token when available; the bucket refills continuously.
func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ipLimiter tracks one bucket per client address and prunes stale entries.
type ipLimiter struct {
	mu            sync.Mutex
	buckets       map[string]*tokenBucket
	ratePerMinute float64
	burst         float64
}

func newIPLimiter(ratePerMinute, burst float64) *ipLimiter {
	return &ipLimiter{
		buckets:       make(map[string]*tokenBucket),
		ratePerMinute: ratePerMinute,
		burst:         burst,
	}
}

// Allow reports whether the caller's address may proceed. It also sweeps
// entries idle for more than ten minutes so the map stays bounded.
func (l *ipLimiter) Allow(r *http.Request) bool {
	ip := remoteIP(r)
	l.mu.Lock()
	if len(l.buckets) > 1024 {
		cutoff := time.Now().Add(-10 * time.Minute)
		for addr, bucket := range l.buckets {
			bucket.mu.Lock()
			idle := bucket.last.Before(cutoff)
			bucket.mu.Unlock()
			if idle {
				delete(l.buckets, addr)
			}
		}
	}
	bucket, ok := l.buckets[ip]
	if !ok {
		bucket = newTokenBucket(l.ratePerMinute, l.burst)
		l.buckets[ip] = bucket
	}
	l.mu.Unlock()
	return bucket.allow()
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimited wraps a handler with a per-address limiter; exhausted callers
// receive 429 with a Retry-After header.
func rateLimited(limiter *ipLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow(r) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error":      "too many requests, try again shortly",
				"request_id": requestIDFrom(r.Context()),
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
