package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// okNext is a handler that records the X-Vortex-Client-IP it observed.
func okNext(seenIP *string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seenIP != nil {
			*seenIP = r.Header.Get(clientIPHeader)
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestEdge_BlockedGets403JSON(t *testing.T) {
	bl, _ := NewBlocklist(BlocklistConfig{IPBlocklist: []string{"6.6.6.6"}})
	e := NewEdge(EdgeConfig{Blocklist: bl})
	h := e.Middleware()(okNext(nil))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "6.6.6.6:1111"
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("403 body not JSON: %v", err)
	}
	if body["error"] != "blocked" || body["reason"] != "manual block" {
		t.Errorf("body = %v, want blocked/manual block", body)
	}
}

func TestEdge_RateLimitedGets429(t *testing.T) {
	rl := NewHTTPRateLimiter(HTTPRateLimiterConfig{RPM: 60, Burst: 1, Enabled: true})
	now := time.Now()
	rl.now = func() time.Time { return now }
	e := NewEdge(EdgeConfig{RateLimit: rl})
	h := e.Middleware()(okNext(nil))

	mk := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "9.9.9.9:1"
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if mk() != http.StatusOK {
		t.Fatal("first request should pass")
	}
	if code := mk(); code != http.StatusTooManyRequests {
		t.Errorf("second request = %d, want 429", code)
	}
}

func TestEdge_AllowedPassesThrough(t *testing.T) {
	e := NewEdge(EdgeConfig{})
	var reached bool
	h := e.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:5"
	h.ServeHTTP(rec, req)
	if !reached || rec.Code != http.StatusOK {
		t.Errorf("allowed request: reached=%v code=%d, want true 200", reached, rec.Code)
	}
}

func TestEdge_SetsClientIPHeader(t *testing.T) {
	e := NewEdge(EdgeConfig{})
	var seen string
	h := e.Middleware()(okNext(&seen))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.5:4444"
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "203.0.113.5" {
		t.Errorf("X-Vortex-Client-IP = %q, want 203.0.113.5", seen)
	}
}

func TestEdge_TrustedProxyUsesXFF(t *testing.T) {
	// The immediate peer is a trusted proxy, so XFF gives the real client.
	e := NewEdge(EdgeConfig{TrustedProxies: []string{"10.0.0.1"}})
	var seen string
	h := e.Middleware()(okNext(&seen))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:9999"
	req.Header.Set("X-Forwarded-For", "198.51.100.23, 10.0.0.1")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "198.51.100.23" {
		t.Errorf("resolved IP = %q, want 198.51.100.23 (from XFF via trusted proxy)", seen)
	}
}

func TestEdge_UntrustedProxyIgnoresXFF(t *testing.T) {
	// The peer is NOT trusted, so a spoofed XFF must be ignored.
	e := NewEdge(EdgeConfig{TrustedProxies: []string{"10.0.0.1"}})
	var seen string
	h := e.Middleware()(okNext(&seen))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.99:1"
	req.Header.Set("X-Forwarded-For", "1.1.1.1") // spoof attempt
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "203.0.113.99" {
		t.Errorf("resolved IP = %q, want 203.0.113.99 (RemoteAddr, XFF ignored)", seen)
	}
}

func TestEdge_StatsReflectOperations(t *testing.T) {
	bl, _ := NewBlocklist(BlocklistConfig{IPBlocklist: []string{"7.7.7.7"}})
	e := NewEdge(EdgeConfig{Blocklist: bl})
	h := e.Middleware()(okNext(nil))

	// One blocked, one allowed.
	for _, ip := range []string{"7.7.7.7:1", "8.8.8.8:1"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = ip
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	s := e.Stats()
	if s.Blocklist.TotalChecked < 2 {
		t.Errorf("TotalChecked = %d, want >= 2", s.Blocklist.TotalChecked)
	}
	if s.Blocklist.ManualBlocks != 0 {
		t.Errorf("ManualBlocks = %d, want 0 (config blocklist is not a runtime manual ban)", s.Blocklist.ManualBlocks)
	}
}
