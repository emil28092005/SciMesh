package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenBucketBurstThenThrottles(t *testing.T) {
	bucket := newTokenBucket(10, 3)
	for i := 0; i < 3; i++ {
		if !bucket.allow() {
			t.Fatalf("request %d must pass within the burst", i)
		}
	}
	if bucket.allow() {
		t.Error("fourth request within the burst must be throttled")
	}
}

func TestRateLimitedReturns429(t *testing.T) {
	limiter := newIPLimiter(10, 2)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := rateLimited(limiter, next)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ui/login", nil)
	req.RemoteAddr = "10.0.0.5:5555"
	// Burst is 2: the first two pass, the third is throttled.
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: got %d", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third request: got %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 must carry Retry-After")
	}
	// A different address is not throttled by the same bucket.
	other := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ui/login", nil)
	other.RemoteAddr = "10.0.0.6:5555"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, other)
	if rec.Code != http.StatusOK {
		t.Errorf("other client: got %d, want 200", rec.Code)
	}
}
