package api

import (
	"context"
	"encoding/json"
	"net/http"
)

// DevOpsServer is a connected server summary (GET /api/devops/servers).
type DevOpsServer struct {
	Name string `json:"name"`
}

// devopsCommandRequest is the POST /api/devops/command body.
type devopsCommandRequest struct {
	Server  string `json:"server"`
	Command string `json:"command"`
}

// SetDevOpsProvider wires the DevOps endpoints. servers lists connected hosts;
// run executes a command on a server (approval-gated server-side). When unset,
// the endpoints return empty / 503.
func (s *Server) SetDevOpsProvider(servers func() []DevOpsServer, run func(ctx context.Context, server, command string) (string, error)) {
	s.devopsServers = servers
	s.devopsRun = run
}

// handleDevOpsServers returns the connected servers.
func (s *Server) handleDevOpsServers(w http.ResponseWriter, _ *http.Request) {
	list := []DevOpsServer{}
	if s.devopsServers != nil {
		if got := s.devopsServers(); got != nil {
			list = got
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"servers": list})
}

// handleDevOpsCommand runs a command on a server via the DevOps agent.
func (s *Server) handleDevOpsCommand(w http.ResponseWriter, r *http.Request) {
	if s.devopsRun == nil {
		http.Error(w, "devops agent not configured", http.StatusServiceUnavailable)
		return
	}
	var req devopsCommandRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Command == "" {
		http.Error(w, "command is required", http.StatusBadRequest)
		return
	}
	out, err := s.devopsRun(r.Context(), req.Server, req.Command)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"output": out})
}
