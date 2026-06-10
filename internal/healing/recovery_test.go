package healing

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// recNotifier records the alerts it is asked to send.
type recNotifier struct {
	mu     sync.Mutex
	sent   []string // "severity|title"
	bodies []string
}

func (n *recNotifier) Send(_ context.Context, severity, title, body string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, severity+"|"+title)
	n.bodies = append(n.bodies, body)
	return nil
}

func (n *recNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.sent)
}

// recAudit records audit entries.
type recAudit struct {
	mu      sync.Mutex
	entries []string // "actor|action|resource"
}

func (a *recAudit) Append(_ context.Context, actor, action, resource string, _ map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, actor+"|"+action+"|"+resource)
	return nil
}

func (a *recAudit) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.entries)
}

func failEvent(name string) HealthEvent {
	return HealthEvent{Name: name, Kind: KindRoute, Healthy: false, Error: "down", Consecutive: 3, Timestamp: time.Now()}
}

func TestRecovery_NotifyAction(t *testing.T) {
	n := &recNotifier{}
	a := &recAudit{}
	m := NewRecoveryManager([]RecoveryRule{
		{CheckName: "api", Action: ActionNotify},
	}, n, a, RecoveryDeps{})

	if err := m.Handle(context.Background(), failEvent("api")); err != nil {
		t.Fatal(err)
	}
	if n.count() != 1 {
		t.Fatalf("expected 1 notification, got %d", n.count())
	}
	if n.sent[0] != "critical|VORTEX Health Alert" {
		t.Errorf("alert = %q, want critical health alert", n.sent[0])
	}
	if a.count() != 1 {
		t.Errorf("expected 1 audit entry, got %d", a.count())
	}
}

func TestRecovery_ReloadConfigAction(t *testing.T) {
	var reloaded bool
	m := NewRecoveryManager([]RecoveryRule{
		{CheckName: "cfg", Action: ActionReloadConfig},
	}, nil, nil, RecoveryDeps{
		ReloadConfig: func(context.Context) error { reloaded = true; return nil },
	})
	if err := m.Handle(context.Background(), failEvent("cfg")); err != nil {
		t.Fatal(err)
	}
	if !reloaded {
		t.Error("ReloadConfig should have been called")
	}
	if s := m.Stats(); s.ActionsExecuted != 1 || s.ActionsFailed != 0 {
		t.Errorf("stats = %+v, want 1 executed / 0 failed", s)
	}
}

func TestRecovery_RestartRouteAction(t *testing.T) {
	var restarted string
	m := NewRecoveryManager([]RecoveryRule{
		{CheckName: "redis", Action: ActionRestartRoute},
	}, nil, nil, RecoveryDeps{
		RestartRoute: func(_ context.Context, name string) error { restarted = name; return nil },
	})
	_ = m.Handle(context.Background(), failEvent("redis"))
	if restarted != "redis" {
		t.Errorf("RestartRoute called with %q, want redis", restarted)
	}
}

func TestRecovery_RespectsCooldown(t *testing.T) {
	var calls int
	m := NewRecoveryManager([]RecoveryRule{
		{CheckName: "api", Action: ActionReloadConfig, Cooldown: time.Hour, MaxRetries: 5},
	}, nil, nil, RecoveryDeps{
		ReloadConfig: func(context.Context) error { calls++; return nil },
	})
	// Two failures back-to-back; the second is within cooldown → skipped.
	_ = m.Handle(context.Background(), failEvent("api"))
	_ = m.Handle(context.Background(), failEvent("api"))
	if calls != 1 {
		t.Errorf("cooldown should prevent the 2nd execution, got %d calls", calls)
	}
}

func TestRecovery_RespectsMaxRetries(t *testing.T) {
	var calls int
	m := NewRecoveryManager([]RecoveryRule{
		{CheckName: "api", Action: ActionReloadConfig, Cooldown: time.Nanosecond, MaxRetries: 2},
	}, nil, nil, RecoveryDeps{
		ReloadConfig: func(context.Context) error { calls++; return nil },
	})
	for i := 0; i < 5; i++ {
		_ = m.Handle(context.Background(), failEvent("api"))
		time.Sleep(time.Millisecond) // clear the nanosecond cooldown
	}
	if calls != 2 {
		t.Errorf("MaxRetries=2 should cap executions at 2, got %d", calls)
	}
}

func TestRecovery_RecoveryEventSendsGreenAlert(t *testing.T) {
	n := &recNotifier{}
	m := NewRecoveryManager([]RecoveryRule{
		{CheckName: "api", Action: ActionNotify},
	}, n, nil, RecoveryDeps{})

	// Fail then recover.
	_ = m.Handle(context.Background(), failEvent("api"))
	rec := HealthEvent{Name: "api", Kind: KindRoute, Healthy: true, Recovered: true, Timestamp: time.Now()}
	_ = m.Handle(context.Background(), rec)

	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.sent) != 2 {
		t.Fatalf("expected fail + recovery alerts, got %d", len(n.sent))
	}
	if n.sent[1] != "info|VORTEX Recovered" {
		t.Errorf("recovery alert = %q, want info recovered", n.sent[1])
	}
	if !contains(n.bodies[1], "Recovered") || !contains(n.bodies[1], "Downtime") {
		t.Errorf("recovery body missing fields: %q", n.bodies[1])
	}
}

func TestRecovery_StatsAccurate(t *testing.T) {
	m := NewRecoveryManager([]RecoveryRule{
		{CheckName: "a", Action: ActionReloadConfig, Cooldown: time.Nanosecond},
		{CheckName: "b", Action: ActionReloadConfig, Cooldown: time.Nanosecond},
	}, nil, nil, RecoveryDeps{
		ReloadConfig: func(context.Context) error { return fmt.Errorf("boom") },
	})
	_ = m.Handle(context.Background(), failEvent("a"))
	_ = m.Handle(context.Background(), failEvent("b"))
	_ = m.Handle(context.Background(), HealthEvent{Name: "c"}) // unmatched → no action

	s := m.Stats()
	if s.TotalEvents != 3 {
		t.Errorf("TotalEvents = %d, want 3", s.TotalEvents)
	}
	if s.ActionsExecuted != 2 || s.ActionsFailed != 2 {
		t.Errorf("executed=%d failed=%d, want 2/2", s.ActionsExecuted, s.ActionsFailed)
	}
}

func TestRecovery_AuditEntryPerAction(t *testing.T) {
	a := &recAudit{}
	m := NewRecoveryManager([]RecoveryRule{
		{CheckName: "api", Action: ActionNotify},
	}, &recNotifier{}, a, RecoveryDeps{})
	_ = m.Handle(context.Background(), failEvent("api"))
	if a.count() != 1 {
		t.Fatalf("expected 1 audit entry, got %d", a.count())
	}
	if a.entries[0] != "healing|auto.recovery|api" {
		t.Errorf("audit entry = %q", a.entries[0])
	}
}

func TestRecovery_UnmatchedEventNoOp(t *testing.T) {
	n := &recNotifier{}
	m := NewRecoveryManager([]RecoveryRule{{CheckName: "api", Action: ActionNotify}}, n, nil, RecoveryDeps{})
	if err := m.Handle(context.Background(), failEvent("unknown")); err != nil {
		t.Fatal(err)
	}
	if n.count() != 0 {
		t.Error("an unmatched event should trigger no action")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
