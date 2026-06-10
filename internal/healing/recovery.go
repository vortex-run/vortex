package healing

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// RecoveryAction is an automatic response to a health event.
type RecoveryAction string

// Recovery actions.
const (
	ActionReloadConfig RecoveryAction = "reload_config"
	ActionRestartRoute RecoveryAction = "restart_route"
	ActionNotify       RecoveryAction = "notify"
	ActionRunScript    RecoveryAction = "run_script"
	ActionScaleUp      RecoveryAction = "scale_up"
)

// RecoveryRule maps a check name to the action taken when it fails.
type RecoveryRule struct {
	CheckName  string
	Action     RecoveryAction
	MaxRetries int           // default 3
	Cooldown   time.Duration // min time between attempts (default 1m)
	ScriptPath string        // for ActionRunScript
	NotifyOnly bool          // alert but do not auto-fix
}

// Notifier sends alerts. Satisfied by *messaging.Router via an adapter (keeps
// the healing package decoupled from messaging).
type Notifier interface {
	Send(ctx context.Context, severity, title, body string) error
}

// AuditLogger records recovery actions. Satisfied by *audit.Log via an adapter.
type AuditLogger interface {
	Append(ctx context.Context, actor, action, resource string, detail map[string]any) error
}

// RecoveryDeps are the side-effecting callbacks for the actions. Any may be nil
// (the action then no-ops with a logged result). This keeps RecoveryManager
// decoupled from proxy/autoscaler/script execution.
type RecoveryDeps struct {
	ReloadConfig func(ctx context.Context) error
	RestartRoute func(ctx context.Context, name string) error
	RunScript    func(ctx context.Context, path string) error
	ScaleUp      func(ctx context.Context) error
}

// RecoveryStats summarises recovery activity.
type RecoveryStats struct {
	TotalEvents     int64     `json:"total_events"`
	ActionsExecuted int64     `json:"actions_executed"`
	ActionsFailed   int64     `json:"actions_failed"`
	LastEvent       time.Time `json:"last_event"`
}

// RecoveryManager matches health events to rules and runs recovery actions,
// respecting per-rule cooldowns and retry limits, and auditing every action.
type RecoveryManager struct {
	rules    map[string]RecoveryRule
	notifier Notifier
	audit    AuditLogger
	deps     RecoveryDeps

	mu      sync.Mutex
	lastRun map[string]time.Time // ruleName → last attempt
	retries map[string]int       // ruleName → attempts in the current failure
	downAt  map[string]time.Time // checkName → first failure time (for downtime)
	statsMu sync.Mutex
	stats   RecoveryStats
}

// NewRecoveryManager constructs a manager. notifier/audit/deps may be nil.
func NewRecoveryManager(rules []RecoveryRule, notifier Notifier, audit AuditLogger, deps RecoveryDeps) *RecoveryManager {
	m := &RecoveryManager{
		rules:    make(map[string]RecoveryRule, len(rules)),
		notifier: notifier,
		audit:    audit,
		deps:     deps,
		lastRun:  map[string]time.Time{},
		retries:  map[string]int{},
		downAt:   map[string]time.Time{},
	}
	for _, r := range rules {
		if r.MaxRetries <= 0 {
			r.MaxRetries = 3
		}
		if r.Cooldown <= 0 {
			r.Cooldown = time.Minute
		}
		m.rules[r.CheckName] = r
	}
	return m
}

// Handle processes a health event: on a failure it runs the matching rule's
// action (subject to cooldown + retry limits); on a recovery it clears state
// and sends a green alert.
func (m *RecoveryManager) Handle(ctx context.Context, event HealthEvent) error {
	m.statsMu.Lock()
	m.stats.TotalEvents++
	m.stats.LastEvent = event.Timestamp
	m.statsMu.Unlock()

	rule, ok := m.rules[event.Name]
	if !ok {
		return nil // nothing to do for this check
	}

	if event.Recovered {
		return m.handleRecovery(ctx, event, rule)
	}
	return m.handleFailure(ctx, event, rule)
}

// handleFailure runs the rule's action, honouring cooldown + retry limits.
func (m *RecoveryManager) handleFailure(ctx context.Context, event HealthEvent, rule RecoveryRule) error {
	m.mu.Lock()
	if _, seen := m.downAt[event.Name]; !seen {
		m.downAt[event.Name] = event.Timestamp
	}
	last := m.lastRun[rule.CheckName]
	if !last.IsZero() && time.Since(last) < rule.Cooldown {
		m.mu.Unlock()
		return nil // within cooldown — skip
	}
	if m.retries[rule.CheckName] >= rule.MaxRetries {
		m.mu.Unlock()
		// Still alert that we've exhausted retries (once).
		return nil
	}
	m.retries[rule.CheckName]++
	m.lastRun[rule.CheckName] = time.Now()
	attempt := m.retries[rule.CheckName]
	m.mu.Unlock()

	result, err := m.execute(ctx, event, rule)
	m.recordExecution(err)
	m.auditAction(ctx, event.Name, string(rule.Action), result, err, attempt)
	return err
}

// handleRecovery clears failure state and sends a recovery alert.
func (m *RecoveryManager) handleRecovery(ctx context.Context, event HealthEvent, rule RecoveryRule) error {
	m.mu.Lock()
	downtime := time.Duration(0)
	if t, ok := m.downAt[event.Name]; ok {
		downtime = event.Timestamp.Sub(t)
		delete(m.downAt, event.Name)
	}
	delete(m.retries, rule.CheckName)
	delete(m.lastRun, rule.CheckName)
	m.mu.Unlock()

	body := fmt.Sprintf("✅ VORTEX Recovered\nCheck: %s is healthy again\nDowntime: %s",
		event.Name, downtime.Round(time.Second))
	_ = m.notify(ctx, "info", "VORTEX Recovered", body)
	m.auditAction(ctx, event.Name, "recovered", "ok", nil, 0)
	return nil
}

// execute performs the rule's action and returns a short result string.
func (m *RecoveryManager) execute(ctx context.Context, event HealthEvent, rule RecoveryRule) (string, error) {
	// NotifyOnly downgrades any action to an alert.
	if rule.NotifyOnly || rule.Action == ActionNotify {
		body := fmt.Sprintf("⚠️ VORTEX Health Alert\nCheck: %s\nError: %s\nConsecutive failures: %d\nTime: %s",
			event.Name, event.Error, event.Consecutive, event.Timestamp.Format(time.RFC3339))
		if err := m.notify(ctx, "critical", "VORTEX Health Alert", body); err != nil {
			return "notify failed", err
		}
		return "notified", nil
	}

	switch rule.Action {
	case ActionReloadConfig:
		if m.deps.ReloadConfig == nil {
			return "reload not wired", nil
		}
		if err := m.deps.ReloadConfig(ctx); err != nil {
			return "reload failed", err
		}
		return "config reloaded", nil

	case ActionRestartRoute:
		if m.deps.RestartRoute == nil {
			return "restart not wired", nil
		}
		if err := m.deps.RestartRoute(ctx, event.Name); err != nil {
			return "restart failed", err
		}
		return "route restarted", nil

	case ActionRunScript:
		if m.deps.RunScript == nil {
			return "script not wired", nil
		}
		if err := m.deps.RunScript(ctx, rule.ScriptPath); err != nil {
			return "script failed", err
		}
		return "script ran", nil

	case ActionScaleUp:
		if m.deps.ScaleUp == nil {
			return "scale not wired", nil
		}
		if err := m.deps.ScaleUp(ctx); err != nil {
			return "scale failed", err
		}
		return "scaled up", nil

	default:
		return "unknown action", nil
	}
}

// recordExecution updates the executed/failed counters.
func (m *RecoveryManager) recordExecution(err error) {
	m.statsMu.Lock()
	m.stats.ActionsExecuted++
	if err != nil {
		m.stats.ActionsFailed++
	}
	m.statsMu.Unlock()

}

// notify sends an alert if a notifier is configured.
func (m *RecoveryManager) notify(ctx context.Context, severity, title, body string) error {
	if m.notifier == nil {
		return nil
	}
	return m.notifier.Send(ctx, severity, title, body)
}

// auditAction records an action to the audit log if configured.
func (m *RecoveryManager) auditAction(ctx context.Context, check, action, result string, err error, attempt int) {
	if m.audit == nil {
		return
	}
	detail := map[string]any{"action": action, "result": result, "attempt": attempt}
	if err != nil {
		detail["error"] = err.Error()
	}
	_ = m.audit.Append(ctx, "healing", "auto.recovery", check, detail)
}

// Stats returns a snapshot of recovery activity.
func (m *RecoveryManager) Stats() RecoveryStats {
	m.statsMu.Lock()
	defer m.statsMu.Unlock()
	return m.stats
}
