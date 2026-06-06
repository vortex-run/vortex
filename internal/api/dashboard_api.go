package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/vortex-run/vortex/internal/audit"
)

// handleStatus returns extended status for the dashboard (GET /api/status).
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	var info StatusInfo
	if s.statusProvider != nil {
		info = s.statusProvider()
	}
	// Always fill in what the API server itself knows.
	cfg := s.holder.Get()
	if info.ClusterName == "" {
		info.ClusterName = cfg.Cluster.Name
	}
	if info.Version == "" {
		info.Version = s.version
	}
	s.writeJSON(w, http.StatusOK, info)
}

// handleSecretsStatus returns declared secrets with set/unset state, never
// their values (GET /api/secrets/status).
func (s *Server) handleSecretsStatus(w http.ResponseWriter, _ *http.Request) {
	var secrets []SecretStatus
	if s.secretsProvider != nil {
		secrets = s.secretsProvider()
	}
	if secrets == nil {
		secrets = []SecretStatus{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"secrets": secrets})
}

// handlePlugins returns installed plugins (GET /api/plugins).
func (s *Server) handlePlugins(w http.ResponseWriter, _ *http.Request) {
	var plugins []PluginInfo
	if s.pluginsProvider != nil {
		plugins = s.pluginsProvider()
	}
	if plugins == nil {
		plugins = []PluginInfo{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"plugins": plugins})
}

// handleAudit returns filtered audit entries (GET /api/audit).
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if s.auditLog == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"entries": []audit.Entry{}})
		return
	}
	filter := audit.QueryFilter{
		Actor:    r.URL.Query().Get("actor"),
		Action:   r.URL.Query().Get("action"),
		Resource: r.URL.Query().Get("resource"),
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Limit = n
		}
	}
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.Since = t
		}
	}
	if v := r.URL.Query().Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.Until = t
		}
	}

	entries, err := s.auditLog.Query(filter)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []audit.Entry{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// handleAuditVerify verifies the audit chain (POST /api/audit/verify).
func (s *Server) handleAuditVerify(w http.ResponseWriter, _ *http.Request) {
	if s.auditLog == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"valid": true})
		return
	}
	if err := s.auditLog.Verify(); err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"valid": true})
}
