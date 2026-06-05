package security

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// EdgeConfig configures the edge security middleware.
type EdgeConfig struct {
	Blocklist      *Blocklist       // may be nil to skip block checks
	RateLimit      *HTTPRateLimiter // may be nil to skip rate limiting
	TrustedProxies []string         // CIDRs/IPs whose X-Forwarded-For is trusted
}

// Edge composes IP blocking and rate limiting into one HTTP middleware. It
// resolves the real client IP (honouring X-Forwarded-For only from trusted
// proxies) and exposes it downstream via the X-Vortex-Client-IP header.
type Edge struct {
	cfg         EdgeConfig
	trustedNets []*net.IPNet
}

// EdgeStats combines blocklist stats with edge-level counters.
type EdgeStats struct {
	Blocklist BlocklistStats
}

// NewEdge builds an Edge from cfg. Invalid TrustedProxies entries are ignored
// (treated as no trusted proxy) so a misconfiguration fails closed — XFF is then
// never trusted.
func NewEdge(cfg EdgeConfig) *Edge {
	trusted, _ := parseCIDRs(cfg.TrustedProxies)
	return &Edge{cfg: cfg, trustedNets: trusted}
}

// Middleware returns the edge security middleware. Pipeline per request:
//
//  1. resolve the client IP (XFF if from a trusted proxy, else RemoteAddr)
//  2. Blocklist.IsAllowed → 403 JSON if blocked
//  3. Blocklist.RecordRequest (auto-ban accounting)
//  4. RateLimit.Allow → 429 if limited
//  5. next handler, with X-Vortex-Client-IP set to the resolved IP
func (e *Edge) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := e.resolveClientIP(r)

			if e.cfg.Blocklist != nil {
				if allowed, reason := e.cfg.Blocklist.IsAllowed(ip); !allowed {
					writeBlocked(w, reason)
					return
				}
				e.cfg.Blocklist.RecordRequest(ip)
			}

			// Expose the resolved IP so the rate limiter and downstream agree.
			r.Header.Set(clientIPHeader, ip)

			if e.cfg.RateLimit != nil && !e.cfg.RateLimit.Allow(ip) {
				retry := e.cfg.RateLimit.retryAfterSeconds()
				w.Header().Set("Retry-After", strconv.Itoa(retry))
				writeRateLimited(w, retry)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// resolveClientIP returns the real client IP. When the immediate peer
// (RemoteAddr) is a trusted proxy, the left-most X-Forwarded-For entry is used;
// otherwise RemoteAddr is authoritative (a spoofed XFF from an untrusted peer is
// ignored).
func (e *Edge) resolveClientIP(r *http.Request) string {
	remote := hostOnly(r.RemoteAddr)
	if len(e.trustedNets) == 0 {
		return remote
	}
	peer := net.ParseIP(remote)
	if peer == nil || !ipInNets(peer, e.trustedNets) {
		return remote
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Left-most entry is the original client.
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if net.ParseIP(first) != nil {
			return first
		}
	}
	return remote
}

// Stats returns the edge's combined statistics.
func (e *Edge) Stats() EdgeStats {
	var s EdgeStats
	if e.cfg.Blocklist != nil {
		s.Blocklist = e.cfg.Blocklist.Stats()
	}
	return s
}

// writeBlocked writes a 403 JSON body naming the block reason.
func writeBlocked(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "blocked", "reason": reason})
}

// writeRateLimited writes a 429 JSON body with the retry hint.
func writeRateLimited(w http.ResponseWriter, retry int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "rate limit exceeded", "retry_after": strconv.Itoa(retry),
	})
}
