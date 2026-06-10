package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/vortex-run/vortex/internal/auth"
)

// createKeyRequest is the POST /api/keys body.
type createKeyRequest struct {
	UserID      string      `json:"user_id"`
	OrgID       string      `json:"org_id"`
	Roles       []auth.Role `json:"roles"`
	Description string      `json:"description"`
	TTLSeconds  int64       `json:"ttl_seconds"` // 0 = never expires
	// RateLimitRPM sets a custom per-minute request budget for this key's
	// identity: 0 keeps the server default, negative makes it unlimited.
	RateLimitRPM int `json:"rate_limit_rpm"`
}

// createKeyResponse returns the new key's public fields plus the one-time
// plaintext secret.
type createKeyResponse struct {
	ID        string      `json:"id"`
	Secret    string      `json:"secret"` // shown once; never retrievable again
	UserID    string      `json:"user_id"`
	OrgID     string      `json:"org_id"`
	Roles     []auth.Role `json:"roles"`
	ExpiresAt time.Time   `json:"expires_at,omitempty"`
}

// handleListKeys returns the API keys for the org named in the ?org= query
// parameter (defaulting to "default"). Hashes are never included.
func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	if s.keys == nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "key store not configured"})
		return
	}
	org := r.URL.Query().Get("org")
	if org == "" {
		org = "default"
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"keys": s.keys.List(org)})
}

// handleCreateKey issues a new API key. The plaintext secret is returned exactly
// once in the response and never stored.
func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	if s.keys == nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "key store not configured"})
		return
	}
	var req createKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.OrgID == "" {
		req.OrgID = "default"
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	key, secret, err := s.keys.Issue(req.UserID, req.OrgID, req.Roles, req.Description, ttl)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "issuing key: " + err.Error()})
		return
	}
	if req.RateLimitRPM != 0 && s.keyLimiter != nil {
		s.keyLimiter.SetKeyLimit(key.UserID, req.RateLimitRPM)
	}
	s.audit(clientIP(r), "apikey.create", key.ID, map[string]any{"org": key.OrgID, "user": key.UserID})
	s.writeJSON(w, http.StatusCreated, createKeyResponse{
		ID:        key.ID,
		Secret:    secret,
		UserID:    key.UserID,
		OrgID:     key.OrgID,
		Roles:     key.Roles,
		ExpiresAt: key.ExpiresAt,
	})
}

// handleRevokeKey revokes the API key whose ID is in the path.
func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	if s.keys == nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "key store not configured"})
		return
	}
	id := r.PathValue("id")
	if id == "" {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing key id"})
		return
	}
	if err := s.keys.Revoke(id); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "revoking key: " + err.Error()})
		return
	}
	s.audit(clientIP(r), "apikey.revoke", id, nil)
	s.writeJSON(w, http.StatusOK, map[string]string{"revoked": id})
}
