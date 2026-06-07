package api

import "net/http"

// SetStudioHandler wires the VORTEX Studio handler tree (mounted under
// /studio/). Studio is a data-plane console (it can run terminals, git, DB
// queries), so it requires a real API key even from localhost — no control-
// plane loopback bypass. When nil, /studio/* returns 404.
//
// The /studio/terminal endpoint is a WebSocket: requireAPIKey checks the key
// (which a browser sends as a query param or header) before the upgrade.
func (s *Server) SetStudioHandler(h http.Handler) { s.studioHandler = h }

// handleStudio dispatches /studio/* to the registered studio handler (behind
// API-key auth) or 404 when Studio is not configured.
func (s *Server) handleStudio(w http.ResponseWriter, r *http.Request) {
	if s.studioHandler == nil {
		http.NotFound(w, r)
		return
	}
	s.requireAPIKey(s.studioHandler).ServeHTTP(w, r)
}
