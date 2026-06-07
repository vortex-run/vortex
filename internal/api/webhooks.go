package api

import (
	"net/http"

	"github.com/vortex-run/vortex/internal/security"
)

// WebhookSpec registers an external-platform webhook handler under /webhook/.
// Each webhook carries its own per-IP rate limit (separate from the management
// API and agent-submit limits) because these endpoints are internet-facing and
// authenticate via their platform's own signature, not an API key.
type WebhookSpec struct {
	Path    string // e.g. "/webhook/telegram"
	Handler http.Handler
	RPM     int // per-IP requests/min (0 → a sane default of 60)
}

// SetWebhooks installs the given webhook handlers. They are matched by exact
// path under the /webhook/ prefix and are NOT protected by API-key auth (each
// verifies its own platform signature); they ARE per-IP rate limited.
func (s *Server) SetWebhooks(specs []WebhookSpec) {
	m := make(map[string]http.Handler, len(specs))
	for _, spec := range specs {
		rpm := spec.RPM
		if rpm <= 0 {
			rpm = 60
		}
		limiter := security.NewHTTPRateLimiter(security.HTTPRateLimiterConfig{
			RPM: rpm, Burst: rpm, Enabled: true,
		})
		h := spec.Handler
		m[spec.Path] = rateLimitWebhook(limiter, h)
	}
	s.webhooks = m
}

// rateLimitWebhook wraps h with a per-IP token-bucket limiter, returning 429
// when exceeded.
func rateLimitWebhook(limiter *security.HTTPRateLimiter, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow(clientIP(r)) {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// handleWebhook dispatches /webhook/* requests to the registered handler for
// the exact path, or 404 when none is registered.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhooks == nil {
		http.NotFound(w, r)
		return
	}
	h, ok := s.webhooks[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.ServeHTTP(w, r)
}
