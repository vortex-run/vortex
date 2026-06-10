package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/vortex-run/vortex/internal/audit"
)

// HealingStatus is the GET /api/healing/status response (M14).
type HealingStatus struct {
	Healthy       bool                 `json:"healthy"`
	Checks        []HealingCheck       `json:"checks"`
	SLOAlerts     []HealingSLOAlert    `json:"slo_alerts"`
	RecoveryStats HealingRecoveryStats `json:"recovery_stats"`
}

// HealingCheck is one monitored check's status.
type HealingCheck struct {
	Name                string    `json:"name"`
	Healthy             bool      `json:"healthy"`
	LatencyMs           int64     `json:"latency_ms"`
	LastCheck           time.Time `json:"last_check"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
}

// HealingSLOAlert is a current SLO breach.
type HealingSLOAlert struct {
	RouteName  string  `json:"route_name"`
	Target     float64 `json:"target"`
	Current    float64 `json:"current"`
	BurnRate   float64 `json:"burn_rate"`
	AlertLevel string  `json:"alert_level"`
}

// HealingRecoveryStats summarises recovery activity.
type HealingRecoveryStats struct {
	TotalEvents     int64 `json:"total_events"`
	ActionsExecuted int64 `json:"actions_executed"`
}

// SetHealingProvider wires the GET /api/healing/status data source. When nil,
// the endpoint returns an empty healthy status.
func (s *Server) SetHealingProvider(fn func() HealingStatus) { s.healingProvider = fn }

// handleHealingStatus returns the current self-healing status.
func (s *Server) handleHealingStatus(w http.ResponseWriter, _ *http.Request) {
	if s.healingProvider == nil {
		s.writeJSON(w, http.StatusOK, HealingStatus{Healthy: true, Checks: []HealingCheck{}, SLOAlerts: []HealingSLOAlert{}})
		return
	}
	s.writeJSON(w, http.StatusOK, s.healingProvider())
}

// handleHealingEvents returns recent healing events from the audit log
// (actor=healing): health failures and recovery actions.
func (s *Server) handleHealingEvents(w http.ResponseWriter, r *http.Request) {
	if s.auditLog == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"events": []audit.Entry{}})
		return
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := s.auditLog.Query(audit.QueryFilter{Actor: "healing", Limit: limit})
	if err != nil {
		http.Error(w, "failed to read audit log", http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"events": entries})
}
