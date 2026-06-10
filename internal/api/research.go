package api

import (
	"net/http"
	"strings"
	"time"
)

// ResearchReport is a saved research report summary (GET /api/research/reports).
type ResearchReport struct {
	Title    string    `json:"title"`
	FilePath string    `json:"file_path"`
	SavedAt  time.Time `json:"saved_at"`
}

// SetResearchProvider wires the research report endpoints. list returns saved
// reports; get returns a report's markdown by filename. When unset, the
// endpoints return empty/404.
func (s *Server) SetResearchProvider(list func() []ResearchReport, get func(name string) (string, bool)) {
	s.researchList = list
	s.researchGet = get
}

// handleResearchReports returns the list of saved research reports.
func (s *Server) handleResearchReports(w http.ResponseWriter, _ *http.Request) {
	if s.researchList == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"reports": []ResearchReport{}})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"reports": s.researchList()})
}

// handleResearchReport returns one report's markdown by filename.
func (s *Server) handleResearchReport(w http.ResponseWriter, r *http.Request) {
	if s.researchGet == nil {
		http.Error(w, "research reports not available", http.StatusNotFound)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/research/reports/")
	md, ok := s.researchGet(name)
	if !ok {
		http.Error(w, "report not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write([]byte(md))
}
