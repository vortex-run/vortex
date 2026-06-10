package healing

import (
	"context"
	"sync"
	"testing"
)

// sloNotifier records SLO alerts.
type sloNotifier struct {
	mu     sync.Mutex
	titles []string
	sevs   []string
	bodies []string
}

func (n *sloNotifier) Send(_ context.Context, severity, title, body string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sevs = append(n.sevs, severity)
	n.titles = append(n.titles, title)
	n.bodies = append(n.bodies, body)
	return nil
}

func (n *sloNotifier) last() (sev, title, body string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.titles) == 0 {
		return "", "", ""
	}
	i := len(n.titles) - 1
	return n.sevs[i], n.titles[i], n.bodies[i]
}

func (n *sloNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.titles)
}

func TestSLO_AddSLORegistersRoute(t *testing.T) {
	tr := NewSLOTracker(nil, nil)
	tr.AddSLO("api", 0.999)
	if len(tr.slos) != 1 || tr.slos[0].route != "api" || tr.slos[0].target != 0.999 {
		t.Errorf("AddSLO did not register: %+v", tr.slos)
	}
}

func TestSLO_HighBurnTriggersPage(t *testing.T) {
	// target 0.999 → budget 0.001. compliance 0.98 → errRate 0.02 → burn 20x > 14.4.
	n := &sloNotifier{}
	tr := NewSLOTracker(func(string) (float64, bool) { return 0.98, true }, n)
	tr.AddSLO("api", 0.999)
	tr.checkAll(context.Background())

	if n.count() != 1 {
		t.Fatalf("expected 1 alert, got %d", n.count())
	}
	sev, title, _ := n.last()
	if title != "SLO BREACH" || sev != "critical" {
		t.Errorf("page alert sev/title = %q/%q", sev, title)
	}
	st := tr.Status()
	if len(st) != 1 || st[0].AlertLevel != AlertPage {
		t.Errorf("status = %+v, want a page-level alert", st)
	}
}

func TestSLO_MediumBurnTriggersCritical(t *testing.T) {
	// target 0.999, compliance 0.992 → errRate 0.008 → burn 8x (6 < 8 <= 14.4).
	n := &sloNotifier{}
	tr := NewSLOTracker(func(string) (float64, bool) { return 0.992, true }, n)
	tr.AddSLO("api", 0.999)
	tr.checkAll(context.Background())
	st := tr.Status()
	if len(st) != 1 || st[0].AlertLevel != AlertCritical {
		t.Errorf("status = %+v, want critical", st)
	}
}

func TestSLO_LowComplianceTriggersWarning(t *testing.T) {
	// target 0.99, compliance 0.989 → errRate 0.011 → burn 1.1x (< 6) but below
	// target → warning.
	n := &sloNotifier{}
	tr := NewSLOTracker(func(string) (float64, bool) { return 0.989, true }, n)
	tr.AddSLO("api", 0.99)
	tr.checkAll(context.Background())
	st := tr.Status()
	if len(st) != 1 || st[0].AlertLevel != AlertWarning {
		t.Errorf("status = %+v, want warning", st)
	}
	if sev, _, _ := n.last(); sev != "warn" {
		t.Errorf("warning severity = %q, want warn", sev)
	}
}

func TestSLO_WithinTargetNoAlert(t *testing.T) {
	n := &sloNotifier{}
	tr := NewSLOTracker(func(string) (float64, bool) { return 0.9995, true }, n)
	tr.AddSLO("api", 0.999)
	tr.checkAll(context.Background())
	if n.count() != 0 {
		t.Error("a route within target should not alert")
	}
	if len(tr.Status()) != 0 {
		t.Error("status should be empty when compliant")
	}
}

func TestSLO_RecoveryClearsAlert(t *testing.T) {
	n := &sloNotifier{}
	compliance := 0.98 // breaching
	tr := NewSLOTracker(func(string) (float64, bool) { return compliance, true }, n)
	tr.AddSLO("api", 0.999)

	tr.checkAll(context.Background()) // breach → page alert
	if len(tr.Status()) != 1 {
		t.Fatal("expected a breach alert")
	}
	compliance = 0.9999 // recover
	tr.checkAll(context.Background())
	if len(tr.Status()) != 0 {
		t.Error("recovery should clear the alert")
	}
	if _, title, _ := n.last(); title != "SLO Recovered" {
		t.Errorf("expected a recovery notification, last title = %q", title)
	}
}

func TestSLO_DeduplicatesAlerts(t *testing.T) {
	n := &sloNotifier{}
	tr := NewSLOTracker(func(string) (float64, bool) { return 0.98, true }, n)
	tr.AddSLO("api", 0.999)
	tr.checkAll(context.Background())
	tr.checkAll(context.Background()) // still breaching
	tr.checkAll(context.Background())
	if n.count() != 1 {
		t.Errorf("a sustained breach should alert once, got %d", n.count())
	}
}

func TestSLO_NoDataNoAlert(t *testing.T) {
	n := &sloNotifier{}
	tr := NewSLOTracker(func(string) (float64, bool) { return 0, false }, n) // no data
	tr.AddSLO("api", 0.999)
	tr.checkAll(context.Background())
	if n.count() != 0 {
		t.Error("no metric data → no alert")
	}
}

func TestSLO_BurnRateMath(t *testing.T) {
	// target 0.999 (budget 0.001), compliance 0.999 → errRate 0.001 → burn 1x.
	if got := burnRate(0.999, 0.999); got < 0.99 || got > 1.01 {
		t.Errorf("burnRate(0.999,0.999) = %v, want ~1", got)
	}
	// Perfect target (budget 0) → 0 burn (avoid div-by-zero).
	if got := burnRate(0.5, 1.0); got != 0 {
		t.Errorf("burnRate with zero budget = %v, want 0", got)
	}
}
