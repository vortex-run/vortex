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

// --- M19 hardening: per-API-key, global, and burst limiters -----------------

// DefaultAPIKeyRPM is the per-key request budget when none is configured.
const DefaultAPIKeyRPM = 1000

// DefaultGlobalRPM is the whole-server request budget when none is configured.
const DefaultGlobalRPM = 10000

// Burst-protection defaults: an IP making more than DefaultBurstThreshold
// requests inside DefaultBurstWindow is banned for DefaultBurstBan.
const (
	DefaultBurstThreshold = 100
	DefaultBurstWindow    = 1 * time.Second
	DefaultBurstBan       = 5 * time.Minute
)

// APIKeyRateLimiterConfig configures a per-API-key rate limiter.
type APIKeyRateLimiterConfig struct {
	DefaultRPM int  // requests per minute per key; <= 0 uses DefaultAPIKeyRPM
	Enabled    bool // when false, Allow always returns true
}

// APIKeyRateLimiter applies a token bucket per authenticated API-key identity.
// Admin keys are never limited; individual keys can carry a custom budget set
// at creation time via SetKeyLimit. Safe for concurrent use.
type APIKeyRateLimiter struct {
	cfg APIKeyRateLimiterConfig

	mu      sync.Mutex
	buckets map[string]*tokenBucket
	limits  map[string]int // key → custom RPM; <= 0 means unlimited
	now     func() time.Time
}

// NewAPIKeyRateLimiter builds a per-key limiter from cfg.
func NewAPIKeyRateLimiter(cfg APIKeyRateLimiterConfig) *APIKeyRateLimiter {
	if cfg.DefaultRPM <= 0 {
		cfg.DefaultRPM = DefaultAPIKeyRPM
	}
	return &APIKeyRateLimiter{
		cfg:     cfg,
		buckets: make(map[string]*tokenBucket),
		limits:  make(map[string]int),
		now:     time.Now,
	}
}

// SetClock overrides the limiter's time source (tests only).
func (l *APIKeyRateLimiter) SetClock(now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now != nil {
		l.now = now
	}
}

// SetKeyLimit assigns a custom per-minute budget to one key. A non-positive
// rpm makes the key unlimited. The key's bucket is reset so the new budget
// takes effect immediately.
func (l *APIKeyRateLimiter) SetKeyLimit(key string, rpm int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.limits[key] = rpm
	delete(l.buckets, key)
}

// Allow reports whether a request by the given key identity may proceed,
// consuming one token. Admin keys and keys with a non-positive custom limit
// are never limited.
func (l *APIKeyRateLimiter) Allow(key string, admin bool) bool {
	if !l.cfg.Enabled || admin {
		return true
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	rpm := l.cfg.DefaultRPM
	if custom, ok := l.limits[key]; ok {
		if custom <= 0 {
			return true
		}
		rpm = custom
	}
	burst := float64(rpm)
	ratePerSec := float64(rpm) / 60.0

	b, ok := l.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: burst, lastSeen: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.lastSeen).Seconds()
		b.tokens = minFloat(burst, b.tokens+elapsed*ratePerSec)
		b.lastSeen = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// GlobalRateLimiter is a single token bucket shared by every request the
// server handles, protecting against distributed floods that stay under any
// per-IP or per-key budget. Safe for concurrent use.
type GlobalRateLimiter struct {
	mu         sync.Mutex
	enabled    bool
	ratePerSec float64
	burst      float64
	tokens     float64
	last       time.Time
	now        func() time.Time
}

// NewGlobalRateLimiter builds a global limiter allowing rpm requests per
// minute across all clients; rpm <= 0 uses DefaultGlobalRPM.
func NewGlobalRateLimiter(rpm int) *GlobalRateLimiter {
	if rpm <= 0 {
		rpm = DefaultGlobalRPM
	}
	return &GlobalRateLimiter{
		enabled:    true,
		ratePerSec: float64(rpm) / 60.0,
		burst:      float64(rpm),
		tokens:     float64(rpm),
		now:        time.Now,
	}
}

// SetClock overrides the limiter's time source (tests only).
func (g *GlobalRateLimiter) SetClock(now func() time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now != nil {
		g.now = now
		g.last = now()
	}
}

// Allow reports whether one more request may proceed, consuming one token.
func (g *GlobalRateLimiter) Allow() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.enabled {
		return true
	}
	now := g.now()
	if !g.last.IsZero() {
		elapsed := now.Sub(g.last).Seconds()
		g.tokens = minFloat(g.burst, g.tokens+elapsed*g.ratePerSec)
	}
	g.last = now

	if g.tokens >= 1 {
		g.tokens--
		return true
	}
	return false
}

// Middleware rejects requests with 429 once the global budget is exhausted.
func (g *GlobalRateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !g.Allow() {
				slog.Default().Warn("global rate limit exceeded",
					"ip", clientIP(r), "path", r.URL.Path)
				w.Header().Set("Retry-After", "1")
				writeRateLimited(w, 1)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BurstProtectionConfig configures BurstProtection. Zero values take the
// DefaultBurst* constants.
type BurstProtectionConfig struct {
	Threshold int           // requests inside Window that trigger a ban
	Window    time.Duration // burst measurement window
	BanFor    time.Duration // how long a triggered IP stays banned
}

// BurstProtection auto-bans IPs that exceed a request burst threshold (by
// default >100 requests within one second bans the IP for five minutes). An
// optional notifier (e.g. Telegram via the notification router) is alerted on
// each triggered ban. Safe for concurrent use.
type BurstProtection struct {
	cfg BurstProtectionConfig

	mu      sync.Mutex
	windows map[string]*requestWindow
	bans    map[string]time.Time // ip → banned-until
	notify  func(title, body string)
	now     func() time.Time
}

// NewBurstProtection builds a BurstProtection from cfg, applying defaults for
// zero fields.
func NewBurstProtection(cfg BurstProtectionConfig) *BurstProtection {
	if cfg.Threshold <= 0 {
		cfg.Threshold = DefaultBurstThreshold
	}
	if cfg.Window <= 0 {
		cfg.Window = DefaultBurstWindow
	}
	if cfg.BanFor <= 0 {
		cfg.BanFor = DefaultBurstBan
	}
	return &BurstProtection{
		cfg:     cfg,
		windows: make(map[string]*requestWindow),
		bans:    make(map[string]time.Time),
		now:     time.Now,
	}
}

// SetClock overrides the protection's time source (tests only).
func (b *BurstProtection) SetClock(now func() time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now != nil {
		b.now = now
	}
}

// SetNotify wires the out-of-band alert callback invoked when a ban triggers.
func (b *BurstProtection) SetNotify(fn func(title, body string)) {
	b.mu.Lock()
	b.notify = fn
	b.mu.Unlock()
}

// Allow records a request from ip and reports whether it may proceed. It
// returns false while ip is banned; crossing the burst threshold triggers the
// ban, a WARN log, and the notifier.
func (b *BurstProtection) Allow(ip string) bool {
	now := b.now()

	b.mu.Lock()
	if until, ok := b.bans[ip]; ok {
		if now.Before(until) {
			b.mu.Unlock()
			return false
		}
		delete(b.bans, ip) // expired
	}

	w, ok := b.windows[ip]
	if !ok {
		w = &requestWindow{}
		b.windows[ip] = w
	}
	cutoff := now.Add(-b.cfg.Window)
	kept := w.times[:0]
	for _, ts := range w.times {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	kept = append(kept, now)
	w.times = kept

	if len(w.times) <= b.cfg.Threshold {
		b.mu.Unlock()
		return true
	}

	// Threshold crossed: ban, reset the window, alert.
	b.bans[ip] = now.Add(b.cfg.BanFor)
	w.times = nil
	notify := b.notify
	b.mu.Unlock()

	slog.Default().Warn("burst protection triggered",
		"ip", ip, "threshold", b.cfg.Threshold,
		"window", b.cfg.Window.String(), "ban", b.cfg.BanFor.String())
	if notify != nil {
		go notify("🚨 VORTEX burst protection",
			"IP "+ip+" exceeded "+strconv.Itoa(b.cfg.Threshold)+
				" requests in "+b.cfg.Window.String()+
				" — banned for "+b.cfg.BanFor.String())
	}
	return false
}

// Middleware rejects requests from banned IPs with 429 and a Retry-After of
// the remaining ban duration (rounded up to a second).
func (b *BurstProtection) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !b.Allow(clientIP(r)) {
				retry := int(b.cfg.BanFor.Seconds())
				if retry < 1 {
					retry = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(retry))
				writeRateLimited(w, retry)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
