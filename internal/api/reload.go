package api

import (
	"encoding/json"
	"net"
	"net/http"
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
