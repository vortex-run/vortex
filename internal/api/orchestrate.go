package api

import (
	"context"
	"encoding/json"
	"net/http"
)

// orchestrateRequest is the POST /api/orchestrate body.
type orchestrateRequest struct {
	Goal string `json:"goal"`
}

// OrchestrateResult is the analyze response.
type OrchestrateResult struct {
	Summary string `json:"summary"`
}

// SetOrchestrateProvider wires POST /api/orchestrate. When unset, the endpoint
// returns 503.
func (s *Server) SetOrchestrateProvider(run func(ctx context.Context, goal string) (string, error)) {
	s.orchestrateRun = run
}

// handleOrchestrate runs a multi-agent orchestration for a goal.
func (s *Server) handleOrchestrate(w http.ResponseWriter, r *http.Request) {
	if s.orchestrateRun == nil {
		http.Error(w, "orchestration agent not configured", http.StatusServiceUnavailable)
		return
	}
	var req orchestrateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Goal == "" {
		http.Error(w, "goal is required", http.StatusBadRequest)
		return
	}
	summary, err := s.orchestrateRun(r.Context(), req.Goal)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeJSON(w, http.StatusOK, OrchestrateResult{Summary: summary})
}
