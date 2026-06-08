package api

import (
	"context"
	"encoding/json"
	"net/http"
)

// ForgeJob mirrors a forge.Job for the API (kept here so api stays decoupled
// from the forge package).
type ForgeJob struct {
	ID       string `json:"id"`
	Message  string `json:"message"`
	State    string `json:"state"`
	Progress string `json:"progress"`
	Error    string `json:"error,omitempty"`
}

// ForgeRuntime is the subset of the forge job manager the API needs. The wiring
// in start.go adapts *forge.JobManager to this interface.
type ForgeRuntime interface {
	Submit(ctx context.Context, message, sessionID string, chatID int64) string
	Get(id string) (ForgeJob, bool)
	List() []ForgeJob
}

// SetForgeRuntime wires the forge job manager backing the /api/forge endpoints.
// When nil, those endpoints return 503.
func (s *Server) SetForgeRuntime(rt ForgeRuntime) { s.forgeRuntime = rt }

// forgeBuildRequest is the POST /api/forge/build body.
type forgeBuildRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id"`
	ChatID    int64  `json:"chat_id"`
}

// handleForgeBuild starts a build asynchronously and returns the job id.
func (s *Server) handleForgeBuild(w http.ResponseWriter, r *http.Request) {
	if s.forgeRuntime == nil {
		http.Error(w, "forge not configured", http.StatusServiceUnavailable)
		return
	}
	var req forgeBuildRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}
	id := s.forgeRuntime.Submit(r.Context(), req.Message, req.SessionID, req.ChatID)
	s.writeJSON(w, http.StatusOK, map[string]any{"job_id": id, "status": "started"})
}

// handleForgeStatus returns the status of a single job.
func (s *Server) handleForgeStatus(w http.ResponseWriter, r *http.Request) {
	if s.forgeRuntime == nil {
		http.Error(w, "forge not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	job, ok := s.forgeRuntime.Get(id)
	if !ok {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	s.writeJSON(w, http.StatusOK, job)
}

// handleForgeJobs returns recent build jobs.
func (s *Server) handleForgeJobs(w http.ResponseWriter, _ *http.Request) {
	if s.forgeRuntime == nil {
		http.Error(w, "forge not configured", http.StatusServiceUnavailable)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"jobs": s.forgeRuntime.List()})
}
