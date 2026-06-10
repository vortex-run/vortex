package proxyhttp

import (
	"bufio"
	"net"
	"net/http"
)

// Default security header values (build plan M19). CSP allows same-origin
// scripts and styles (plus inline styles, which the dashboard SPA needs).
const (
	hstsValue        = "max-age=31536000; includeSubDomains; preload"
	cspValue         = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'"
	permissionsValue = "geolocation=(), camera=(), microphone=()"
)

// SecurityHeadersConfig configures the security headers middleware.
type SecurityHeadersConfig struct {
	// Version, when non-empty, is advertised as X-Vortex-Version. Off by
	// default: version disclosure aids attackers fingerprinting deployments.
	Version string
}

// SecurityHeadersMiddleware returns a middleware that hardens every HTTP
// response with the standard security headers and strips server
// identification (Server, X-Powered-By). Defaults only: headers already set
// by the handler (e.g. an upstream app's own CSP) are preserved, except the
// identification headers which are always removed.
func SecurityHeadersMiddleware() func(http.Handler) http.Handler {
	return NewSecurityHeaders(SecurityHeadersConfig{})
}

// NewSecurityHeaders builds the security headers middleware from cfg. The
// headers are applied when the response is written (not before the handler
// runs) so they also cover headers copied from proxied upstream responses.
func NewSecurityHeaders(cfg SecurityHeadersConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(&securityHeaderWriter{
				ResponseWriter: w,
				cfg:            cfg,
				tls:            r.TLS != nil,
			}, r)
		})
	}
}

// securityHeaderWriter applies the security headers exactly once, immediately
// before the first byte of the response is written — after the handler (or
// reverse proxy) has populated the header map, so upstream identification
// headers can be stripped and upstream security headers preserved.
type securityHeaderWriter struct {
	http.ResponseWriter
	cfg     SecurityHeadersConfig
	tls     bool
	applied bool
}

// apply mutates the pending header map. Must be called before the first
// WriteHeader/Write.
func (w *securityHeaderWriter) apply() {
	if w.applied {
		return
	}
	w.applied = true
	h := w.Header()

	// Strip server identification, wherever it came from.
	h.Del("Server")
	h.Del("X-Powered-By")

	setIfAbsent(h, "X-Content-Type-Options", "nosniff")
	setIfAbsent(h, "X-Frame-Options", "DENY")
	setIfAbsent(h, "X-XSS-Protection", "1; mode=block")
	setIfAbsent(h, "Referrer-Policy", "strict-origin-when-cross-origin")
	setIfAbsent(h, "Permissions-Policy", permissionsValue)
	setIfAbsent(h, "Content-Security-Policy", cspValue)
	// HSTS is only meaningful (and only spec-permitted) over TLS.
	if w.tls {
		setIfAbsent(h, "Strict-Transport-Security", hstsValue)
	}
	if w.cfg.Version != "" {
		setIfAbsent(h, "X-Vortex-Version", w.cfg.Version)
	}
}

func setIfAbsent(h http.Header, key, value string) {
	if h.Get(key) == "" {
		h.Set(key, value)
	}
}

func (w *securityHeaderWriter) WriteHeader(code int) {
	w.apply()
	w.ResponseWriter.WriteHeader(code)
}

func (w *securityHeaderWriter) Write(b []byte) (int, error) {
	w.apply() // implicit 200 path: Write without a prior WriteHeader
	return w.ResponseWriter.Write(b)
}

// Hijack forwards to the underlying Hijacker so WebSocket upgrades work.
func (w *securityHeaderWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hj.Hijack()
}

// Flush forwards to the underlying Flusher so streaming responses still flush.
func (w *securityHeaderWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
