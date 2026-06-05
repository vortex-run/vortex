package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fixedClock returns a controllable clock and a function to advance it.
func fixedClock(start time.Time) (func() time.Time, func(time.Duration)) {
	cur := start
	return func() time.Time { return cur }, func(d time.Duration) { cur = cur.Add(d) }
}

func TestRateLimit_FirstRequestAllowed(t *testing.T) {
	r := NewHTTPRateLimiter(HTTPRateLimiterConfig{RPM: 60, Burst: 5, Enabled: true})
	if !r.Allow("1.1.1.1") {
		t.Error("first request should be allowed")
	}
}

func TestRateLimit_BurstExhausted(t *testing.T) {
	now, _ := fixedClock(time.Now())
	r := NewHTTPRateLimiter(HTTPRateLimiterConfig{RPM: 60, Burst: 3, Enabled: true})
	r.now = now // freeze time so no refill happens

	for i := 0; i < 3; i++ {
		if !r.Allow("1.1.1.1") {
			t.Fatalf("request %d within burst should be allowed", i)
		}
	}
	if r.Allow("1.1.1.1") {
		t.Error("4th request should be denied after burst exhausted")
	}
}

func TestRateLimit_RefillAfterTime(t *testing.T) {
	now, advance := fixedClock(time.Now())
	r := NewHTTPRateLimiter(HTTPRateLimiterConfig{RPM: 60, Burst: 1, Enabled: true})
	r.now = now

	if !r.Allow("1.1.1.1") {
		t.Fatal("first request allowed")
	}
	if r.Allow("1.1.1.1") {
		t.Fatal("second immediate request should be denied")
	}
	// 60 RPM = 1 token/sec; advance 1s to refill one token.
	advance(time.Second)
	if !r.Allow("1.1.1.1") {
		t.Error("request after 1s refill should be allowed")
	}
}

func TestRateLimit_IndependentBuckets(t *testing.T) {
	now, _ := fixedClock(time.Now())
	r := NewHTTPRateLimiter(HTTPRateLimiterConfig{RPM: 60, Burst: 1, Enabled: true})
	r.now = now

	if !r.Allow("1.1.1.1") || !r.Allow("2.2.2.2") {
		t.Fatal("each IP's first request should be allowed")
	}
	if r.Allow("1.1.1.1") {
		t.Error("1.1.1.1 should be limited")
	}
	// 2.2.2.2 already spent its only token too; a third IP is still fine.
	if !r.Allow("3.3.3.3") {
		t.Error("3.3.3.3 has its own full bucket")
	}
}

func TestRateLimit_DisabledAlwaysAllows(t *testing.T) {
	r := NewHTTPRateLimiter(HTTPRateLimiterConfig{RPM: 1, Burst: 1, Enabled: false})
	for i := 0; i < 100; i++ {
		if !r.Allow("1.1.1.1") {
			t.Fatal("disabled limiter must always allow")
		}
	}
}

func TestRateLimit_Middleware429JSON(t *testing.T) {
	now, _ := fixedClock(time.Now())
	r := NewHTTPRateLimiter(HTTPRateLimiterConfig{RPM: 60, Burst: 1, Enabled: true})
	r.now = now
	h := r.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	mk := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "9.9.9.9:1234"
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := mk(); rec.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", rec.Code)
	}
	rec := mk()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 body not JSON: %v", err)
	}
	if body["error"] != "rate limit exceeded" {
		t.Errorf("error field = %q", body["error"])
	}
}

func TestRateLimit_MiddlewareRetryAfterHeader(t *testing.T) {
	now, _ := fixedClock(time.Now())
	r := NewHTTPRateLimiter(HTTPRateLimiterConfig{RPM: 60, Burst: 1, Enabled: true})
	r.now = now
	h := r.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "8.8.8.8:1"
	h.ServeHTTP(httptest.NewRecorder(), req) // consume the token

	rec := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "8.8.8.8:1"
	h.ServeHTTP(rec, req2)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 response must set Retry-After header")
	}
}

func TestPerRouteRateLimiter_DifferentLimits(t *testing.T) {
	now, _ := fixedClock(time.Now())
	p := NewPerRouteRateLimiter(map[string]HTTPRateLimiterConfig{
		"strict": {RPM: 60, Burst: 1, Enabled: true},
		"loose":  {RPM: 6000, Burst: 100, Enabled: true},
	})
	for _, lim := range p.limiters {
		lim.now = now
	}

	pass := func(route string) int {
		h := p.MiddlewareForRoute(route)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		ok := 0
		for i := 0; i < 5; i++ {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "7.7.7.7:1"
			h.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK {
				ok++
			}
		}
		return ok
	}
	if got := pass("strict"); got != 1 {
		t.Errorf("strict route allowed %d/5, want 1", got)
	}
	if got := pass("loose"); got != 5 {
		t.Errorf("loose route allowed %d/5, want 5", got)
	}
	// An unknown route passes everything through (no limiter).
	if got := pass("unknown"); got != 5 {
		t.Errorf("unknown route allowed %d/5, want 5 (passthrough)", got)
	}
}

func TestRateLimit_CleanupRemovesStaleBuckets(t *testing.T) {
	now, advance := fixedClock(time.Now())
	r := NewHTTPRateLimiter(HTTPRateLimiterConfig{RPM: 60, Burst: 5, Enabled: true})
	r.now = now

	r.Allow("1.1.1.1")
	r.Allow("2.2.2.2")
	if len(r.buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(r.buckets))
	}

	// Advance past the idle TTL and sweep; both stale buckets should be removed.
	advance(bucketIdleTTL + time.Minute)
	r.sweep()
	if len(r.buckets) != 0 {
		t.Errorf("buckets after cleanup = %d, want 0", len(r.buckets))
	}
}
