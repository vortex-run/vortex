package api

import (
	"net/http"

	"github.com/sandeepbollavaram/vortex/internal/a2a"
)

// handleAgentCatalog returns the static capability catalog used for
// discovery/selection (GET /api/agents/catalog). This is registry data only —
// it does not imply those agents are running. Auth-gated like other agent APIs.
func (s *Server) handleAgentCatalog(w http.ResponseWriter, _ *http.Request) {
	list := a2a.DefaultCatalog().List()
	out := make([]map[string]any, 0, len(list))
	for _, m := range list {
		out = append(out, map[string]any{
			"id":                 m.ID,
			"name":               m.Name,
			"role":               m.Role,
			"description":        m.Description,
			"version":            m.Version,
			"capabilities":       m.Capabilities,
			"tools":              m.Tools,
			"permission_profile": m.PermissionProfile,
			"eval_notes":         m.EvalNotes,
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}
