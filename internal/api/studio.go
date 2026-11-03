package api

import "net/http"

// SetStudioHandler wires the VORTEX Studio handler tree (mounted under
// /studio/). Studio is a data-plane console (it can run terminals, git, DB
// queries), so it requires a real API key even from localhost — no control-
// plane loopback bypass. When nil, /studio/* returns 404.
//
// The /studio/terminal endpoint is a WebSocket: the key is checked (from the
// Authorization or X-API-Key header) before the upgrade.
func (s *Server) SetStudioHandler(h http.Handler) { s.studioHandler = h }

// handleStudio dispatches /studio/* to the registered studio handler or 404
// when Studio is not configured. It requires the admin role: the terminal is
// a shell, and git push and DB queries run, without the agent's approval
// gate, so a viewer or operator key must not reach it (it would be more power
// than issuing API keys, which is already admin-only).
func (s *Server) handleStudio(w http.ResponseWriter, r *http.Request) {
	if s.studioHandler == nil {
		http.NotFound(w, r)
		return
	}
	s.protectedAdmin(s.studioHandler).ServeHTTP(w, r)
}
