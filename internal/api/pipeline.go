package api

import (
	"context"
	"encoding/json"
	"net/http"
)

// pipelineAnalyzeRequest is the POST /api/pipeline/analyze body.
type pipelineAnalyzeRequest struct {
	Source  string `json:"source"`  // optional URL
	Data    string `json:"data"`    // optional inline CSV/JSON
	Request string `json:"request"` // natural-language analysis request
}

// PipelineResultInfo is the analyze response.
type PipelineResultInfo struct {
	Summary   string   `json:"summary"`
	DataPath  string   `json:"data_path"`
	ChartPath string   `json:"chart_path"`
	Rows      int      `json:"rows"`
	Columns   []string `json:"columns"`
}

// SetPipelineProvider wires POST /api/pipeline/analyze. When unset, the endpoint
// returns 503.
func (s *Server) SetPipelineProvider(analyze func(ctx context.Context, source, data, request string) (PipelineResultInfo, error)) {
	s.pipelineAnalyze = analyze
}

// handlePipelineAnalyze runs a data analysis via the pipeline agent.
func (s *Server) handlePipelineAnalyze(w http.ResponseWriter, r *http.Request) {
	if s.pipelineAnalyze == nil {
		http.Error(w, "pipeline agent not configured", http.StatusServiceUnavailable)
		return
	}
	var req pipelineAnalyzeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Request == "" {
		http.Error(w, "request is required", http.StatusBadRequest)
		return
	}
	if req.Source == "" && req.Data == "" {
		http.Error(w, "source or data is required", http.StatusBadRequest)
		return
	}
	res, err := s.pipelineAnalyze(r.Context(), req.Source, req.Data, req.Request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}
