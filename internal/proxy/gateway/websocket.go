// Package proxygateway implements VORTEX's protocol gateway (build plan M2.6):
// it inspects each request and dispatches WebSocket upgrades, gRPC calls, and
// plain HTTP to the appropriate handler. WebSocket connections are tunnelled
// with M2.1's tcp.Tunnel; gRPC is proxied transparently over HTTP/2. Standard
// library only.
package proxygateway

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/vortex-run/vortex/internal/proxy/tcp"
)

// IsWebSocketUpgrade reports whether req is a WebSocket upgrade handshake: an
// "Upgrade: websocket" header and a Connection header listing "Upgrade" (both
// case-insensitive, per RFC 6455).
func IsWebSocketUpgrade(req *http.Request) bool {
	if !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, tok := range strings.Split(req.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
			return true
		}
	}
	return false
}

// ProxyWebSocket proxies a WebSocket connection: it dials the backend, replays
// the upgrade handshake, relays the backend's 101 response to the client, then
// tunnels bytes bidirectionally with tcp.Tunnel. The client connection is
// hijacked, so w must implement http.Hijacker. backendURL is the upstream base
// URL (scheme is ignored; host:port is used).
func ProxyWebSocket(w http.ResponseWriter, req *http.Request, backendURL string, pool *tcp.Pool) error {
	if !IsWebSocketUpgrade(req) {
		return errors.New("proxygateway: not a websocket upgrade request")
	}

	host, err := hostPort(backendURL)
	if err != nil {
		return err
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket proxy requires a hijackable connection", http.StatusInternalServerError)
		return errors.New("proxygateway: ResponseWriter is not an http.Hijacker")
	}

	// Dial the backend through the TCP pool (or directly if no pool).
	var backendConn net.Conn
	if pool != nil {
		backendConn, err = pool.Get(req.Context(), "tcp", host)
	} else {
		var d net.Dialer
		backendConn, err = d.DialContext(req.Context(), "tcp", host)
	}
	if err != nil {
		http.Error(w, "websocket backend unavailable", http.StatusBadGateway)
		return fmt.Errorf("proxygateway: dialing backend %s: %w", host, err)
	}
	defer func() {
		if pool != nil {
			pool.Put(backendConn, host)
		} else {
			_ = backendConn.Close()
		}
	}()

	// Forward the upgrade request to the backend, preserving the WS headers and
	// adding X-Forwarded-For.
	outreq := buildBackendUpgrade(req)
	if err := outreq.Write(backendConn); err != nil {
		return fmt.Errorf("proxygateway: writing upgrade to backend: %w", err)
	}

	// Read the backend's response to the upgrade.
	br := bufio.NewReader(backendConn)
	resp, err := http.ReadResponse(br, outreq)
	if err != nil {
		return fmt.Errorf("proxygateway: reading backend upgrade response: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body := fmt.Sprintf("backend did not upgrade (status %d)", resp.StatusCode)
		http.Error(w, body, http.StatusBadGateway)
		_ = resp.Body.Close()
		return fmt.Errorf("proxygateway: backend upgrade failed: %s", resp.Status)
	}

	// Hijack the client connection and relay the 101 response to it.
	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		return fmt.Errorf("proxygateway: hijacking client conn: %w", err)
	}
	defer func() { _ = clientConn.Close() }()

	if err := resp.Write(clientBuf); err != nil {
		return fmt.Errorf("proxygateway: writing 101 to client: %w", err)
	}
	if err := clientBuf.Flush(); err != nil {
		return fmt.Errorf("proxygateway: flushing 101 to client: %w", err)
	}

	// If the backend reader buffered any frames after the 101, forward them
	// before handing off to the tunnel.
	if n := br.Buffered(); n > 0 {
		pending, _ := br.Peek(n)
		if _, err := clientConn.Write(pending); err != nil {
			return fmt.Errorf("proxygateway: relaying buffered frames: %w", err)
		}
	}

	// Bidirectional byte tunnel (reuses M2.1).
	return tcp.Tunnel(req.Context(), clientConn, backendConn, tcp.TunnelConfig{})
}

// buildBackendUpgrade clones the WebSocket handshake request for the backend,
// keeping the handshake-relevant headers and adding X-Forwarded-For.
func buildBackendUpgrade(req *http.Request) *http.Request {
	out := &http.Request{
		Method:     http.MethodGet,
		URL:        &url.URL{Path: req.URL.Path, RawQuery: req.URL.RawQuery},
		Host:       req.Host,
		Header:     make(http.Header),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
	}
	// Required upgrade headers plus any optional WS negotiation headers.
	for _, h := range []string{
		"Upgrade", "Connection",
		"Sec-WebSocket-Key", "Sec-WebSocket-Version",
		"Sec-WebSocket-Extensions", "Sec-WebSocket-Protocol",
	} {
		if v := req.Header.Values(h); len(v) > 0 {
			out.Header[h] = v
		}
	}
	if clientIP := clientIP(req.RemoteAddr); clientIP != "" {
		if prior := req.Header.Get("X-Forwarded-For"); prior != "" {
			out.Header.Set("X-Forwarded-For", prior+", "+clientIP)
		} else {
			out.Header.Set("X-Forwarded-For", clientIP)
		}
	}
	return out
}

// hostPort extracts a host:port dial target from a backend URL or bare
// host:port string.
func hostPort(backendURL string) (string, error) {
	if strings.Contains(backendURL, "://") {
		u, err := url.Parse(backendURL)
		if err != nil {
			return "", fmt.Errorf("proxygateway: parsing backend URL: %w", err)
		}
		if u.Host == "" {
			return "", errors.New("proxygateway: backend URL has no host")
		}
		return u.Host, nil
	}
	if backendURL == "" {
		return "", errors.New("proxygateway: empty backend address")
	}
	return backendURL, nil
}

// clientIP extracts the IP from a host:port RemoteAddr.
func clientIP(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// StickySession maps a client IP to the backend it was first routed to, so a
// WebSocket client reconnects to the same backend. Safe for concurrent use.
type StickySession struct {
	mu       sync.RWMutex
	sessions map[string]string // clientIP -> backendAddr
}

// NewStickySession returns an empty sticky-session table.
func NewStickySession() *StickySession {
	return &StickySession{sessions: make(map[string]string)}
}

// Get returns the sticky backend for clientIP, and whether one is set.
func (s *StickySession) Get(clientIP string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	addr, ok := s.sessions[clientIP]
	return addr, ok
}

// Set records the backend for clientIP.
func (s *StickySession) Set(clientIP, backendAddr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[clientIP] = backendAddr
}

// Delete removes the sticky entry for clientIP.
func (s *StickySession) Delete(clientIP string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, clientIP)
}
