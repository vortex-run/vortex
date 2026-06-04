// Package proxyhttp implements VORTEX's L7 (HTTP/1.1 + HTTP/2) reverse proxy
// (build plan M2.2): a pooled RoundTripper, request router, load balancers, the
// proxy handler, and the internet-facing server. It is named proxyhttp to avoid
// clashing with the standard library's net/http. Standard library plus
// golang.org/x/net (already an indirect dependency) only.
package proxyhttp

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/vortex-run/vortex/internal/proxy/tcp"
)

// Defaults for RoundTripperConfig.
const (
	defaultRTDialTimeout  = 10 * time.Second
	defaultRTMaxIdleConns = 100
	defaultRTIdleTimeout  = 90 * time.Second
)

// hopByHopHeaders are connection-specific headers that must not be forwarded by
// a proxy (RFC 7230 §6.1). They are stripped from both requests and responses.
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te", // canonicalized "TE"
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// RoundTripperConfig configures a PooledRoundTripper.
type RoundTripperConfig struct {
	// Pool supplies backend connections via its dialer. Optional; when nil a
	// default dialer is used.
	Pool *tcp.Pool
	// DialTimeout bounds new backend dials. Default 10s.
	DialTimeout time.Duration
	// MaxIdleConns caps idle keep-alive connections per host. Default 100.
	MaxIdleConns int
	// IdleTimeout closes idle keep-alive connections after this. Default 90s.
	IdleTimeout time.Duration
}

// PooledRoundTripper is an http.RoundTripper that forwards requests to backends
// over an internal net/http Transport (which owns HTTP keep-alive and HTTP/2
// connection reuse), after stripping hop-by-hop headers and injecting the
// X-Forwarded-* family. When a tcp.Pool is supplied its dialer acquires
// connections; otherwise a standard dialer is used.
type PooledRoundTripper struct {
	transport *http.Transport
	pool      *tcp.Pool
}

// NewRoundTripper builds a PooledRoundTripper from cfg (zero fields take
// defaults).
func NewRoundTripper(cfg RoundTripperConfig) *PooledRoundTripper {
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = defaultRTDialTimeout
	}
	if cfg.MaxIdleConns <= 0 {
		cfg.MaxIdleConns = defaultRTMaxIdleConns
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultRTIdleTimeout
	}

	rt := &PooledRoundTripper{pool: cfg.Pool}
	dialer := &net.Dialer{Timeout: cfg.DialTimeout}
	rt.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// When a pool is configured, borrow through it so backend dials
			// share the pool's dialer settings; the Transport then manages the
			// connection's keep-alive lifetime.
			if rt.pool != nil {
				return rt.pool.Get(ctx, network, addr)
			}
			return dialer.DialContext(ctx, network, addr)
		},
		MaxIdleConns:        cfg.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.MaxIdleConns,
		IdleConnTimeout:     cfg.IdleTimeout,
		ForceAttemptHTTP2:   true,
		DisableCompression:  false,
	}
	return rt
}

// RoundTrip cleans hop-by-hop headers, injects forwarding headers, and forwards
// the request via the internal transport. Response header cleaning happens in
// the handler.
func (rt *PooledRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	removeHopByHop(req.Header)
	rt.injectForwardedHeaders(req)
	return rt.transport.RoundTrip(req)
}

// injectForwardedHeaders sets the X-Forwarded-* and X-Real-IP headers from the
// inbound request, appending to any existing X-Forwarded-For chain.
func (rt *PooledRoundTripper) injectForwardedHeaders(req *http.Request) {
	clientIP := clientIP(req.RemoteAddr)

	if clientIP != "" {
		if prior := req.Header.Get("X-Forwarded-For"); prior != "" {
			req.Header.Set("X-Forwarded-For", prior+", "+clientIP)
		} else {
			req.Header.Set("X-Forwarded-For", clientIP)
		}
		// X-Real-IP carries only the immediate client; do not overwrite if a
		// trusted upstream already set it.
		if req.Header.Get("X-Real-IP") == "" {
			req.Header.Set("X-Real-IP", clientIP)
		}
	}

	proto := "http"
	if req.TLS != nil {
		proto = "https"
	}
	req.Header.Set("X-Forwarded-Proto", proto)

	if req.Host != "" {
		req.Header.Set("X-Forwarded-Host", req.Host)
	}
}

// removeHopByHop strips the standard hop-by-hop headers plus any header named in
// the Connection header's value (RFC 7230 §6.1).
func removeHopByHop(h http.Header) {
	// Headers explicitly listed in Connection are also hop-by-hop.
	for _, v := range h.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
}

// clientIP extracts the IP portion from a "host:port" RemoteAddr, returning the
// whole string if it has no port.
func clientIP(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}
