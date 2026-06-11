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

// --- M19 hardening: APIKeyRateLimiter / GlobalRateLimiter / BurstProtection --

func TestAPIKeyLimiter_LimitsPerKey(t *testing.T) {
	now, _ := fixedClock(time.Now())
	l := NewAPIKeyRateLimiter(APIKeyRateLimiterConfig{DefaultRPM: 3, Enabled: true})
	l.SetClock(now)

	for i := 0; i < 3; i++ {
		if !l.Allow("key-a", false) {
			t.Fatalf("request %d within budget should be allowed", i)
		}
	}
	if l.Allow("key-a", false) {
		t.Error("request over budget should be denied")
	}
	// Independent bucket: a different key is unaffected.
	if !l.Allow("key-b", false) {
		t.Error("other key should have its own budget")
	}
}

func TestAPIKeyLimiter_AdminUnlimited(t *testing.T) {
	now, _ := fixedClock(time.Now())
	l := NewAPIKeyRateLimiter(APIKeyRateLimiterConfig{DefaultRPM: 1, Enabled: true})
	l.SetClock(now)

	for i := 0; i < 100; i++ {
		if !l.Allow("admin-key", true) {
			t.Fatalf("admin request %d should never be limited", i)
		}
	}
}

func TestAPIKeyLimiter_CustomKeyLimit(t *testing.T) {
	now, _ := fixedClock(time.Now())
	l := NewAPIKeyRateLimiter(APIKeyRateLimiterConfig{DefaultRPM: 1, Enabled: true})
	l.SetClock(now)
	l.SetKeyLimit("big", 5)

	for i := 0; i < 5; i++ {
		if !l.Allow("big", false) {
			t.Fatalf("request %d within custom budget should be allowed", i)
		}
	}
	if l.Allow("big", false) {
		t.Error("request over custom budget should be denied")
	}
}

func TestAPIKeyLimiter_NonPositiveCustomLimitUnlimited(t *testing.T) {
	now, _ := fixedClock(time.Now())
	l := NewAPIKeyRateLimiter(APIKeyRateLimiterConfig{DefaultRPM: 1, Enabled: true})
	l.SetClock(now)
	l.SetKeyLimit("free", -1)

	for i := 0; i < 50; i++ {
		if !l.Allow("free", false) {
			t.Fatalf("request %d for unlimited key should be allowed", i)
		}
	}
}

func TestAPIKeyLimiter_RefillAfterTime(t *testing.T) {
	now, advance := fixedClock(time.Now())
	l := NewAPIKeyRateLimiter(APIKeyRateLimiterConfig{DefaultRPM: 60, Enabled: true})
	l.SetClock(now)

	for i := 0; i < 60; i++ {
		if !l.Allow("k", false) {
			t.Fatalf("request %d within budget should be allowed", i)
		}
	}
	if l.Allow("k", false) {
		t.Fatal("budget should be exhausted")
	}
	advance(time.Second) // 60 RPM = 1 token/sec
	if !l.Allow("k", false) {
		t.Error("request after refill should be allowed")
	}
}

func TestAPIKeyLimiter_DisabledAlwaysAllows(t *testing.T) {
	l := NewAPIKeyRateLimiter(APIKeyRateLimiterConfig{DefaultRPM: 1, Enabled: false})
	for i := 0; i < 10; i++ {
		if !l.Allow("k", false) {
			t.Fatal("disabled limiter should always allow")
		}
	}
}

func TestGlobalLimiter_AppliesAcrossAllRequests(t *testing.T) {
	now, _ := fixedClock(time.Now())
	g := NewGlobalRateLimiter(3)
	g.SetClock(now)

	// Budget is shared: different "clients" drain the same bucket.
	for i := 0; i < 3; i++ {
		if !g.Allow() {
			t.Fatalf("request %d within global budget should be allowed", i)
		}
	}
	if g.Allow() {
		t.Error("request over global budget should be denied")
	}
}

func TestGlobalLimiter_RefillAfterTime(t *testing.T) {
	now, advance := fixedClock(time.Now())
	g := NewGlobalRateLimiter(60)
	g.SetClock(now)

	for i := 0; i < 60; i++ {
		if !g.Allow() {
			t.Fatalf("request %d within budget should be allowed", i)
		}
	}
	if g.Allow() {
		t.Fatal("budget should be exhausted")
	}
	advance(time.Second)
	if !g.Allow() {
		t.Error("request after refill should be allowed")
	}
}

func TestGlobalLimiter_Middleware429(t *testing.T) {
	now, _ := fixedClock(time.Now())
	g := NewGlobalRateLimiter(1)
	g.SetClock(now)

	h := g.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 should carry Retry-After")
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
}

func TestBurstProtection_TriggersOnRapidRequests(t *testing.T) {
	now, _ := fixedClock(time.Now())
	b := NewBurstProtection(BurstProtectionConfig{Threshold: 5, Window: time.Second, BanFor: 5 * time.Minute})
	b.SetClock(now)

	for i := 0; i < 5; i++ {
		if !b.Allow("9.9.9.9") {
			t.Fatalf("request %d under threshold should be allowed", i)
		}
	}
	if b.Allow("9.9.9.9") {
		t.Error("request crossing threshold should be denied and trigger a ban")
	}
	// Still banned on subsequent requests.
	if b.Allow("9.9.9.9") {
		t.Error("banned IP should stay denied")
	}
	// Other IPs unaffected.
	if !b.Allow("8.8.8.8") {
		t.Error("other IP should be unaffected by the ban")
	}
}

func TestBurstProtection_BanExpires(t *testing.T) {
	now, advance := fixedClock(time.Now())
	b := NewBurstProtection(BurstProtectionConfig{Threshold: 2, Window: time.Second, BanFor: 5 * time.Minute})
	b.SetClock(now)

	b.Allow("9.9.9.9")
	b.Allow("9.9.9.9")
	if b.Allow("9.9.9.9") {
		t.Fatal("threshold crossing should be denied")
	}
	advance(5*time.Minute + time.Second)
	if !b.Allow("9.9.9.9") {
		t.Error("request after ban expiry should be allowed")
	}
}

func TestBurstProtection_SlowRequestsNeverTrigger(t *testing.T) {
	now, advance := fixedClock(time.Now())
	b := NewBurstProtection(BurstProtectionConfig{Threshold: 3, Window: time.Second, BanFor: time.Minute})
	b.SetClock(now)

	// 10 requests spaced 2s apart never have >3 inside any 1s window.
	for i := 0; i < 10; i++ {
		if !b.Allow("7.7.7.7") {
			t.Fatalf("paced request %d should be allowed", i)
		}
		advance(2 * time.Second)
	}
}

func TestBurstProtection_NotifierCalled(t *testing.T) {
	now, _ := fixedClock(time.Now())
	b := NewBurstProtection(BurstProtectionConfig{Threshold: 1, Window: time.Second, BanFor: time.Minute})
	b.SetClock(now)

	notified := make(chan string, 1)
	b.SetNotify(func(title, body string) { notified <- title + " " + body })

	b.Allow("6.6.6.6")
	if b.Allow("6.6.6.6") {
		t.Fatal("threshold crossing should be denied")
	}
	select {
	case msg := <-notified:
		if msg == "" {
			t.Error("notification should not be empty")
		}
	case <-time.After(2 * time.Second):
		t.Error("notifier was not called on ban")
	}
}

func TestBurstProtection_Middleware429(t *testing.T) {
	now, _ := fixedClock(time.Now())
	b := NewBurstProtection(BurstProtectionConfig{Threshold: 1, Window: time.Second, BanFor: time.Minute})
	b.SetClock(now)

	h := b.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "5.5.5.5:1234"

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("banned request: got %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 should carry Retry-After")
	}
}

func TestAPIKeyLimiter_SweepRemovesIdleBuckets(t *testing.T) {
	now, advance := fixedClock(time.Now())
	l := NewAPIKeyRateLimiter(APIKeyRateLimiterConfig{DefaultRPM: 10, Enabled: true})
	l.SetClock(now)

	l.Allow("k1", false)
	l.Allow("k2", false)
	if got := len(l.buckets); got != 2 {
		t.Fatalf("buckets = %d, want 2", got)
	}
	advance(20 * time.Minute)
	l.Sweep(bucketIdleTTL)
	if got := len(l.buckets); got != 0 {
		t.Errorf("after sweep buckets = %d, want 0", got)
	}
}

func TestBurstProtection_SweepRemovesIdleWindows(t *testing.T) {
	now, advance := fixedClock(time.Now())
	b := NewBurstProtection(BurstProtectionConfig{Threshold: 5, Window: time.Second, BanFor: time.Minute})
	b.SetClock(now)

	b.Allow("1.2.3.4")
	if len(b.windows) != 1 {
		t.Fatalf("windows = %d, want 1", len(b.windows))
	}
	advance(time.Minute)
	b.Sweep()
	if len(b.windows) != 0 {
		t.Errorf("after sweep windows = %d, want 0", len(b.windows))
	}
}

func TestBurstProtection_SweepRemovesExpiredBans(t *testing.T) {
	now, advance := fixedClock(time.Now())
	b := NewBurstProtection(BurstProtectionConfig{Threshold: 1, Window: time.Second, BanFor: time.Minute})
	b.SetClock(now)

	b.Allow("9.9.9.9")
	b.Allow("9.9.9.9") // triggers ban
	if len(b.bans) != 1 {
		t.Fatalf("bans = %d, want 1", len(b.bans))
	}
	advance(2 * time.Minute)
	b.Sweep()
	if len(b.bans) != 0 {
		t.Errorf("after sweep bans = %d, want 0", len(b.bans))
	}
}
