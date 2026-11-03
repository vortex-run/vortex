package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// localhostOnly reports whether the request originates from the loopback
// interface. The /internal/* endpoints are control-plane operations and must
// never be reachable from off-box; they are the Windows-safe equivalents of
// SIGHUP/SIGTERM used by the vortex reload/stop commands.
func localhostOnly(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loopbackTrusted reports whether r may use the control-plane loopback
// bypass. Coming from loopback isn't enough, because a browser on this
// machine also connects from loopback on behalf of any website: a page could
// POST /internal/shutdown, and a DNS-rebinding page (its own name resolving
// to 127.0.0.1) could read /api/audit. So the request must also name a
// loopback host and not be a cross-site browser request. The CLI (no Origin
// or Sec-Fetch-* headers, Host 127.0.0.1) and the dashboard opened at
// localhost (same-origin) still qualify; anything else needs a key.
func loopbackTrusted(r *http.Request) bool {
	if !localhostOnly(r) || !loopbackHost(r.Host) {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			return false
		}
	}
	return true
}

// loopbackHost reports whether a Host header value (host or host:port) names
// this machine's loopback interface.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// clientIP returns the request's source IP (host portion of RemoteAddr) for
// audit attribution, falling back to the raw RemoteAddr.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// writeJSON writes v as a JSON response with the given status code.
func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Error("encoding internal response", "err", err)
	}
}

// handleInternalReload re-reads and re-validates config via the registered
// reload callback. Localhost-only. Used by `vortex reload` on Windows.
func (s *Server) handleInternalReload(w http.ResponseWriter, r *http.Request) {
	if !localhostOnly(r) {
		s.log.Warn("rejected non-local /internal/reload", "remote", r.RemoteAddr)
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: localhost only"})
		return
	}
	if s.reloadFunc == nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload not supported"})
		return
	}
	if err := s.reloadFunc(); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload failed: " + err.Error()})
		return
	}
	hash := s.holder.Get().Hash()
	s.audit(clientIP(r), "config.reload", "config", map[string]any{"hash": hash})
	s.writeJSON(w, http.StatusOK, map[string]any{
		"reloaded": true,
		"hash":     hash,
	})
}

// handleInternalShutdown begins a graceful shutdown via the registered
// callback. Localhost-only. Used by `vortex stop` on Windows. It responds 200
// before triggering shutdown so the caller receives confirmation.
func (s *Server) handleInternalShutdown(w http.ResponseWriter, r *http.Request) {
	if !localhostOnly(r) {
		s.log.Warn("rejected non-local /internal/shutdown", "remote", r.RemoteAddr)
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: localhost only"})
		return
	}
	if s.shutdownFunc == nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "shutdown not supported"})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"shutdown": "initiated"})
	// Trigger after responding so the client sees the 200.
	go s.shutdownFunc()
}
