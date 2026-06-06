// Package security implements VORTEX's edge protection (build plan M3.7): an
// HTTP-layer token-bucket rate limiter keyed per client IP, an IP
// allowlist/blocklist with Tor-exit and auto-ban support, and an edge
// middleware that composes them. It is distinct from the M2.5 UDP rate limiter,
// which guards the UDP data plane. Standard library only.
package security

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// cleanupInterval is how often StartCleanup sweeps idle buckets.
const cleanupInterval = 5 * time.Minute

// bucketIdleTTL is how long an unused bucket is retained before cleanup removes
// it (it is recreated full on the next request from that IP).
const bucketIdleTTL = 10 * time.Minute

// HTTPRateLimiterConfig configures a per-IP HTTP rate limiter.
type HTTPRateLimiterConfig struct {
	RPM     int  // requests per minute per IP
	Burst   int  // bucket capacity (max burst); defaults to RPM when <= 0
	Enabled bool // when false, Allow always returns true
}

// tokenBucket is a single IP's bucket. Tokens refill continuously at ratePerSec.
type tokenBucket struct {
	tokens   float64
	lastSeen time.Time
}

// HTTPRateLimiter applies a token bucket per source IP. It is safe for
// concurrent use.
type HTTPRateLimiter struct {
	cfg        HTTPRateLimiterConfig
	ratePerSec float64
	burst      float64

	mu      sync.Mutex
	buckets map[string]*tokenBucket
	now     func() time.Time // injectable clock for tests
}

// NewHTTPRateLimiter builds a limiter from cfg. A non-positive Burst defaults to
// RPM (one minute's worth of tokens).
func NewHTTPRateLimiter(cfg HTTPRateLimiterConfig) *HTTPRateLimiter {
	burst := cfg.Burst
	if burst <= 0 {
		burst = cfg.RPM
	}
	return &HTTPRateLimiter{
		cfg:        cfg,
		ratePerSec: float64(cfg.RPM) / 60.0,
		burst:      float64(burst),
		buckets:    make(map[string]*tokenBucket),
		now:        time.Now,
	}
}

// SetClock overrides the limiter's time source. It is intended for tests so
// rate-limit behaviour can be asserted deterministically without real waits.
func (r *HTTPRateLimiter) SetClock(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now != nil {
		r.now = now
	}
}

// Allow reports whether a request from ip may proceed, consuming one token. When
// the limiter is disabled it always allows.
func (r *HTTPRateLimiter) Allow(ip string) bool {
	if !r.cfg.Enabled {
		return true
	}
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()

	b, ok := r.buckets[ip]
	if !ok {
		// New bucket starts full, then immediately spends one token.
		b = &tokenBucket{tokens: r.burst, lastSeen: now}
		r.buckets[ip] = b
	} else {
		// Refill proportionally to elapsed time, capped at burst.
		elapsed := now.Sub(b.lastSeen).Seconds()
		b.tokens = minFloat(r.burst, b.tokens+elapsed*r.ratePerSec)
		b.lastSeen = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// retryAfterSeconds returns how long until at least one token is available for a
// fully-drained bucket, used to populate the Retry-After header.
func (r *HTTPRateLimiter) retryAfterSeconds() int {
	if r.ratePerSec <= 0 {
		return 60
	}
	s := int(1.0 / r.ratePerSec)
	if s < 1 {
		s = 1
	}
	return s
}

// Middleware returns an HTTP middleware that rejects over-limit requests with a
// 429 and a JSON body, setting the Retry-After header. It logs a WARN on each
// rejection. The client IP is read from the X-Vortex-Client-IP header set by the
// edge (see edge.go); absent that it falls back to RemoteAddr.
func (r *HTTPRateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ip := clientIP(req)
			if r.Allow(ip) {
				next.ServeHTTP(w, req)
				return
			}
			retry := r.retryAfterSeconds()
			slog.Default().Warn("rate limit exceeded",
				"ip", ip, "route", routeName(req), "path", req.URL.Path)
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":       "rate limit exceeded",
				"retry_after": strconv.Itoa(retry),
			})
		})
	}
}

// StartCleanup periodically removes buckets that have been idle beyond
// bucketIdleTTL, bounding memory under churning client IPs. It returns when ctx
// is cancelled.
func (r *HTTPRateLimiter) StartCleanup(ctx context.Context) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sweep()
		}
	}
}

// sweep removes idle buckets. Exposed for tests via StartCleanup's tick.
func (r *HTTPRateLimiter) sweep() {
	cutoff := r.now().Add(-bucketIdleTTL)
	r.mu.Lock()
	for ip, b := range r.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(r.buckets, ip)
		}
	}
	r.mu.Unlock()
}

// PerRouteRateLimiter holds an independent HTTPRateLimiter per route name.
type PerRouteRateLimiter struct {
	limiters map[string]*HTTPRateLimiter
}

// NewPerRouteRateLimiter builds a limiter per route from the given configs.
func NewPerRouteRateLimiter(routes map[string]HTTPRateLimiterConfig) *PerRouteRateLimiter {
	m := make(map[string]*HTTPRateLimiter, len(routes))
	for name, cfg := range routes {
		m[name] = NewHTTPRateLimiter(cfg)
	}
	return &PerRouteRateLimiter{limiters: m}
}

// MiddlewareForRoute returns the rate-limit middleware for routeName, or a
// pass-through when the route has no configured limiter.
func (p *PerRouteRateLimiter) MiddlewareForRoute(routeName string) func(http.Handler) http.Handler {
	lim, ok := p.limiters[routeName]
	if !ok {
		return func(next http.Handler) http.Handler { return next }
	}
	return lim.Middleware()
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// clientIPHeader carries the edge-resolved client IP between middlewares so the
// rate limiter and downstream handlers agree on the source IP.
const clientIPHeader = "X-Vortex-Client-IP"

// clientIP returns the request's client IP: the edge-resolved header if present,
// otherwise the host portion of RemoteAddr.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get(clientIPHeader); ip != "" {
		return ip
	}
	return hostOnly(r.RemoteAddr)
}

// hostOnly strips the port from a host:port address, returning the input
// unchanged if it has no port.
func hostOnly(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// routeName returns the route name a request is associated with, if the edge or
// proxy stamped one into the X-Vortex-Route header; otherwise "".
func routeName(r *http.Request) string {
	return r.Header.Get("X-Vortex-Route")
}
