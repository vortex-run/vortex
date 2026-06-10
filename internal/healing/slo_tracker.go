package healing

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Alert levels for an SLO breach.
const (
	AlertWarning  = "warning"
	AlertCritical = "critical"
	AlertPage     = "page"
)

// SLOAlert describes a current SLO state for a route.
type SLOAlert struct {
	RouteName  string  `json:"route_name"`
	Target     float64 `json:"target"`      // e.g. 0.999
	Current    float64 `json:"current"`     // current compliance (0..1)
	BurnRate   float64 `json:"burn_rate"`   // multiples of the budget burn
	AlertLevel string  `json:"alert_level"` // "warning"|"critical"|"page"
}

// ComplianceFunc returns the current success ratio (0..1) for a route. The
// wiring supplies it from the metrics layer; healing stays decoupled.
type ComplianceFunc func(route string) (compliance float64, ok bool)

// sloDef is one tracked objective.
type sloDef struct {
	route  string
	target float64
}

// SLOTracker periodically checks SLO compliance and alerts on breaches.
type SLOTracker struct {
	compliance ComplianceFunc
	notifier   Notifier
	interval   time.Duration

	mu      sync.Mutex
	slos    []sloDef
	alerts  map[string]SLOAlert // route → current alert (only breaching routes)
	alerted map[string]bool     // route → already alerted (de-dup until recovery)
}

// NewSLOTracker constructs a tracker. compliance/notifier may be nil.
func NewSLOTracker(compliance ComplianceFunc, notifier Notifier) *SLOTracker {
	return &SLOTracker{
		compliance: compliance,
		notifier:   notifier,
		interval:   5 * time.Minute,
		alerts:     map[string]SLOAlert{},
		alerted:    map[string]bool{},
	}
}

// AddSLO registers a route with a compliance target (e.g. 0.999).
func (t *SLOTracker) AddSLO(route string, target float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.slos = append(t.slos, sloDef{route: route, target: target})
}

// Start checks every route on the tracker's interval until ctx is cancelled.
func (t *SLOTracker) Start(ctx context.Context) {
	ticker := time.NewTicker(t.interval)
	go func() {
		defer ticker.Stop()
		t.checkAll(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.checkAll(ctx)
			}
		}
	}()
}

// checkAll evaluates every registered SLO once.
func (t *SLOTracker) checkAll(ctx context.Context) {
	t.mu.Lock()
	slos := append([]sloDef(nil), t.slos...)
	t.mu.Unlock()
	for _, s := range slos {
		t.checkOne(ctx, s)
	}
}

// checkOne evaluates a single SLO, firing or clearing an alert.
func (t *SLOTracker) checkOne(ctx context.Context, s sloDef) {
	if t.compliance == nil {
		return
	}
	current, ok := t.compliance(s.route)
	if !ok {
		return // no data yet
	}
	burn := burnRate(current, s.target)
	level, breaching := classify(current, s.target, burn)

	t.mu.Lock()
	if !breaching {
		// Recovered: clear and, if previously alerted, send a recovery note.
		wasAlerted := t.alerted[s.route]
		delete(t.alerts, s.route)
		delete(t.alerted, s.route)
		t.mu.Unlock()
		if wasAlerted {
			t.send(ctx, "info", "SLO Recovered",
				fmt.Sprintf("🟢 SLO recovered: %s route back within target (%.2f%% ≥ %.2f%%).",
					s.route, current*100, s.target*100))
		}
		return
	}
	alert := SLOAlert{RouteName: s.route, Target: s.target, Current: current, BurnRate: burn, AlertLevel: level}
	t.alerts[s.route] = alert
	firstTime := !t.alerted[s.route]
	t.alerted[s.route] = true
	t.mu.Unlock()

	if firstTime {
		t.send(ctx, severityFor(level), "SLO BREACH", formatBreach(alert))
	}
}

// burnRate returns how fast the error budget is being consumed: the route's
// error rate divided by the allowed error budget (1 - target).
func burnRate(current, target float64) float64 {
	budget := 1 - target
	if budget <= 0 {
		return 0
	}
	errRate := 1 - current
	return errRate / budget
}

// classify maps compliance + burn rate to an alert level. Page > Critical >
// Warning. Returns breaching=false when within target.
func classify(current, target, burn float64) (level string, breaching bool) {
	switch {
	case burn > 14.4: // 1h budget burn → page
		return AlertPage, true
	case burn > 6: // 6h budget burn → critical
		return AlertCritical, true
	case current < target: // below target → warning
		return AlertWarning, true
	default:
		return "", false
	}
}

// severityFor maps an alert level to a notifier severity.
func severityFor(level string) string {
	switch level {
	case AlertPage, AlertCritical:
		return "critical"
	default:
		return "warn"
	}
}

// formatBreach renders the alert body.
func formatBreach(a SLOAlert) string {
	emoji := "🟠"
	if a.AlertLevel == AlertCritical || a.AlertLevel == AlertPage {
		emoji = "🔴"
	}
	depleteHrs := 0.0
	if a.BurnRate > 0 {
		depleteHrs = 720 / a.BurnRate // 30-day budget window, hours until depleted
	}
	return fmt.Sprintf("%s SLO BREACH: %s route (%s)\nTarget: %.1f%% | Current: %.1f%%\nBurn rate: %.1fx — budget depleted in ~%.0fh\nAction required!",
		emoji, a.RouteName, a.AlertLevel, a.Target*100, a.Current*100, a.BurnRate, depleteHrs)
}

// send dispatches an alert if a notifier is configured.
func (t *SLOTracker) send(ctx context.Context, severity, title, body string) {
	if t.notifier == nil {
		return
	}
	_ = t.notifier.Send(ctx, severity, title, body)
}

// Status returns the current breaching alerts.
func (t *SLOTracker) Status() []SLOAlert {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]SLOAlert, 0, len(t.alerts))
	for _, a := range t.alerts {
		out = append(out, a)
	}
	return out
}
