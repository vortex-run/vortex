package api

import (
	"encoding/json"
	"net/http"
)

// handleListNamespaces returns all namespaces (GET /api/namespaces).
func (s *Server) handleListNamespaces(w http.ResponseWriter, _ *http.Request) {
	var list []NamespaceInfo
	if s.nsLister != nil {
		list = s.nsLister()
	}
	if list == nil {
		list = []NamespaceInfo{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"namespaces": list})
}

// handleCreateNamespace creates a namespace (POST /api/namespaces).
func (s *Server) handleCreateNamespace(w http.ResponseWriter, r *http.Request) {
	if s.nsCreator == nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "namespaces not configured"})
		return
	}
	var ni NamespaceInfo
	if err := json.NewDecoder(r.Body).Decode(&ni); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := s.nsCreator(ni); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusCreated, ni)
}

// handleDeleteNamespace removes a namespace (DELETE /api/namespaces/{id}).
func (s *Server) handleDeleteNamespace(w http.ResponseWriter, r *http.Request) {
	if s.nsDeleter == nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "namespaces not configured"})
		return
	}
	id := r.PathValue("id")
	if err := s.nsDeleter(id); err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"deleted": id})
}

// handleNamespaceStats returns a namespace's usage (GET /api/namespaces/{id}/stats).
func (s *Server) handleNamespaceStats(w http.ResponseWriter, r *http.Request) {
	if s.nsStats == nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "namespaces not configured"})
		return
	}
	id := r.PathValue("id")
	stats, ok := s.nsStats(id)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "namespace not found"})
		return
	}
	s.writeJSON(w, http.StatusOK, stats)
}
