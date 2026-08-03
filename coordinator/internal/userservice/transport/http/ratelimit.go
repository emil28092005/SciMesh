package http

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// The same per-address token-bucket policy as the coordinator transport:
// login is the credential brute-force surface, the exchange the only public
// token-minting one.
const (
	loginRatePerMinute    = 10
	loginBurst            = 5
	exchangeRatePerMinute = 30
	exchangeBurst         = 10
)

type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	rate   float64
	burst  float64
}

func newTokenBucket(ratePerMinute, burst float64) *tokenBucket {
	return &tokenBucket{tokens: burst, last: time.Now(), rate: ratePerMinute / 60, burst: burst}
}

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

type ipLimiter struct {
	mu            sync.Mutex
	buckets       map[string]*tokenBucket
	ratePerMinute float64
	burst         float64
}

func newIPLimiter(ratePerMinute, burst float64) *ipLimiter {
	return &ipLimiter{buckets: map[string]*tokenBucket{}, ratePerMinute: ratePerMinute, burst: burst}
}

func (l *ipLimiter) Allow(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
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
	bucket, ok := l.buckets[host]
	if !ok {
		bucket = newTokenBucket(l.ratePerMinute, l.burst)
		l.buckets[host] = bucket
	}
	l.mu.Unlock()
	return bucket.allow()
}

func rateLimited(limiter *ipLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow(r) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, errorResponse{
				Error:     "too many requests, try again shortly",
				RequestID: requestIDFrom(r.Context()),
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
