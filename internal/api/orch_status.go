package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/vortex-run/vortex/internal/orchestration"
)

const (
	defaultOrchRunLimit  = 50
	maxOrchRunLimit      = 100
	defaultOrchTaskLimit = 200
	maxOrchTaskLimit     = 500
)

// OrchRunSummary is one orchestration run in GET /api/orchestration/runs.
type OrchRunSummary struct {
	ID        string `json:"id"`
	Goal      string `json:"goal"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

// OrchTaskInfo mirrors orchestration task fields for GET /api/orchestration/runs/{id}/tasks.
//
// Exposure contract (security review, data plane): both GET /api/orchestration
// routes are wrapped in requireAPIKey — any valid API key, regardless of RBAC
// role, and no loopback bypass. They carry NO per-user/org/session scoping.
// That is deliberately the same contract as the rest of the agent data plane
// (GET /api/agents/history/{id} returns full conversation turns, GET
// /api/research/reports/{name} returns the full report, GET /api/agents/comms
// returns the inter-agent feed), and the role-gated surface in this server is
// limited to key and namespace management (protectedAdmin). Operators should
// treat an issued key as read access to all agent output on the instance.
//
// What the fields hold in practice (see internal/orchestration + start.go):
//   - Input: the planner's per-step instruction derived from the run goal
//     (the upstream-context prefix built by buildInput is NOT persisted).
//   - Result: the raw string the routed agent returned — a model completion
//     for general/build_app tasks, a research report for research tasks, or
//     the DevOps agent's reply (which can include remote command output) for
//     devops tasks. Treat it as sensitive as the agents' own history.
//   - Error: the executor/verifier error message.
//   - Meta: planner/verifier bookkeeping only (required_capabilities,
//     permission_profile, verification_status/error, max_attempts,
//     verification_criteria).
type OrchTaskInfo struct {
	ID                   string         `json:"id"`
	Name                 string         `json:"name"`
	AgentType            string         `json:"agent_type"`
	Input                string         `json:"input,omitempty"`
	DependsOn            []string       `json:"depends_on,omitempty"`
	State                string         `json:"state"`
	Result               string         `json:"result,omitempty"`
	Error                string         `json:"error,omitempty"`
	Meta                 map[string]any `json:"meta,omitempty"`
	MaxAttempts          int            `json:"max_attempts,omitempty"`
	Attempts             int            `json:"attempts,omitempty"`
	VerificationCriteria string         `json:"verification_criteria,omitempty"`
	StartedAt            time.Time      `json:"started_at,omitempty"`
	FinishedAt           time.Time      `json:"finished_at,omitempty"`
}

// OrchStoreReader reads persisted orchestration run state. Implemented by
// OrchRunStoreAdapter when a RunStore is wired at startup; nil on Server yields
// empty list responses.
type OrchStoreReader interface {
	ListRecent(limit int) ([]OrchRunSummary, error)
	LoadRunTasks(runID string) ([]OrchTaskInfo, error)
}

// OrchRunStoreAdapter adapts orchestration.RunStore to OrchStoreReader without
// pulling orchestration types into handlers.
type OrchRunStoreAdapter struct {
	Store *orchestration.RunStore
}

// ListRecent returns recent runs (running, done, and failed), newest first.
func (a *OrchRunStoreAdapter) ListRecent(limit int) ([]OrchRunSummary, error) {
	if a == nil || a.Store == nil {
		return nil, nil
	}
	runs, err := a.Store.ListRecent(limit)
	if err != nil {
		return nil, err
	}
	out := make([]OrchRunSummary, 0, len(runs))
	for _, r := range runs {
		status := r.Status
		if status == "" {
			status = orchestration.RunRunning
		}
		out = append(out, OrchRunSummary{
			ID:        r.ID,
			Goal:      r.Goal,
			SessionID: r.SessionID,
			Status:    status,
		})
	}
	return out, nil
}

// LoadRunTasks returns plan-ordered tasks for a run.
func (a *OrchRunStoreAdapter) LoadRunTasks(runID string) ([]OrchTaskInfo, error) {
	if a == nil || a.Store == nil {
		return nil, nil
	}
	tasks, _, err := a.Store.LoadRun(runID)
	if err != nil {
		return nil, err
	}
	out := make([]OrchTaskInfo, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, mapOrchTask(t))
	}
	return out, nil
}

func mapOrchTask(t *orchestration.Task) OrchTaskInfo {
	if t == nil {
		return OrchTaskInfo{}
	}
	return OrchTaskInfo{
		ID:                   t.ID,
		Name:                 t.Name,
		AgentType:            t.AgentType,
		Input:                t.Input,
		DependsOn:            t.DependsOn,
		State:                string(t.State),
		Result:               t.Result,
		Error:                t.Error,
		Meta:                 t.Meta,
		MaxAttempts:          t.MaxAttempts,
		Attempts:             t.Attempts,
		VerificationCriteria: t.VerificationCriteria,
		StartedAt:            t.StartedAt,
		FinishedAt:           t.FinishedAt,
	}
}

// SetOrchStoreProvider wires GET /api/orchestration/* readers. When nil,
// endpoints return empty collections.
func (s *Server) SetOrchStoreProvider(store OrchStoreReader) { s.orchStore = store }

func (s *Server) handleOrchRuns(w http.ResponseWriter, r *http.Request) {
	if s.orchStore == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"runs": []OrchRunSummary{}})
		return
	}
	limit := parseBoundedLimit(r.URL.Query().Get("limit"), defaultOrchRunLimit, maxOrchRunLimit)
	runs, err := s.orchStore.ListRecent(limit)
	if err != nil {
		http.Error(w, "failed to list orchestration runs", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleOrchRunTasks(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		http.Error(w, "run id required", http.StatusBadRequest)
		return
	}
	if s.orchStore == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "tasks": []OrchTaskInfo{}})
		return
	}
	limit := parseBoundedLimit(r.URL.Query().Get("limit"), defaultOrchTaskLimit, maxOrchTaskLimit)
	tasks, err := s.orchStore.LoadRunTasks(runID)
	if err != nil {
		http.Error(w, "failed to load orchestration tasks", http.StatusInternalServerError)
		return
	}
	if len(tasks) > limit {
		tasks = tasks[:limit]
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "tasks": tasks})
}

func parseBoundedLimit(raw string, defaultLimit, maxLimit int) int {
	limit := defaultLimit
	if raw == "" {
		return limit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return limit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}
