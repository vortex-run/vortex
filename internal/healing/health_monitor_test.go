package healing

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeProber returns a scripted error sequence per check name, then sticks on
// the last value. It is safe for concurrent use.
type fakeProber struct {
	mu      sync.Mutex
	seq     map[string][]error // remaining results per check
	latency time.Duration
}

func (p *fakeProber) probe(_ context.Context, c HealthCheck) (time.Duration, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.seq[c.Name]
	if len(s) == 0 {
		return p.latency, nil
	}
	err := s[0]
	if len(s) > 1 {
		p.seq[c.Name] = s[1:]
	}
	return p.latency, err
}

// newTestMonitor builds a monitor with a fake prober and tiny intervals.
func newTestMonitor(seq map[string][]error, checks ...HealthCheck) *Monitor {
	m := NewMonitor(checks)
	m.probe = &fakeProber{seq: seq, latency: 3 * time.Millisecond}
	return m
}

func TestNewMonitor_AppliesDefaults(t *testing.T) {
	m := NewMonitor([]HealthCheck{{Name: "x", Kind: KindRoute, Target: ":1"}})
	if m.checks[0].Interval != 30*time.Second || m.checks[0].Timeout != 5*time.Second || m.checks[0].Threshold != 3 {
		t.Errorf("defaults not applied: %+v", m.checks[0])
	}
}

func TestMonitor_HealthyEmitsNoEvent(t *testing.T) {
	m := newTestMonitor(nil, HealthCheck{Name: "ok", Kind: KindRoute, Target: ":1", Interval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	select {
	case e := <-m.Events():
		t.Errorf("healthy check should emit no event, got %+v", e)
	case <-time.After(60 * time.Millisecond):
	}
	if !m.Healthy() {
		t.Error("monitor should report healthy")
	}
}

func TestMonitor_FailingEmitsAfterThreshold(t *testing.T) {
	// Always fails; threshold 3 → event after the 3rd consecutive failure.
	m := newTestMonitor(map[string][]error{"db": {fmt.Errorf("down")}},
		HealthCheck{Name: "db", Kind: KindRoute, Target: ":1", Interval: 5 * time.Millisecond, Threshold: 3})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	select {
	case e := <-m.Events():
		if e.Healthy || e.Recovered || e.Consecutive < 3 {
			t.Errorf("expected failure event with >=3 consecutive, got %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no failure event emitted")
	}
	if m.Healthy() {
		t.Error("monitor should report unhealthy")
	}
}

func TestMonitor_RecoveryEmitsRecoveredEvent(t *testing.T) {
	// 3 failures (cross threshold) then healthy → failure event then recovery.
	seq := map[string][]error{"api": {fmt.Errorf("e"), fmt.Errorf("e"), fmt.Errorf("e"), nil}}
	m := newTestMonitor(seq,
		HealthCheck{Name: "api", Kind: KindRoute, Target: ":1", Interval: 5 * time.Millisecond, Threshold: 3})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	var sawFail, sawRecover bool
	deadline := time.After(2 * time.Second)
	for !sawRecover {
		select {
		case e := <-m.Events():
			if !e.Healthy && !e.Recovered {
				sawFail = true
			}
			if e.Recovered {
				sawRecover = true
			}
		case <-deadline:
			t.Fatalf("did not see recovery (fail=%v recover=%v)", sawFail, sawRecover)
		}
	}
	if !sawFail {
		t.Error("should have seen a failure event before recovery")
	}
}

func TestMonitor_StatusReturnsAllResults(t *testing.T) {
	m := newTestMonitor(map[string][]error{"b": {fmt.Errorf("x")}},
		HealthCheck{Name: "a", Kind: KindRoute, Target: ":1", Interval: 5 * time.Millisecond},
		HealthCheck{Name: "b", Kind: KindRoute, Target: ":1", Interval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	time.Sleep(60 * time.Millisecond)
	st := m.Status()
	if len(st) != 2 {
		t.Fatalf("status should have 2 checks, got %d", len(st))
	}
	if !st["a"].Healthy || st["b"].Healthy {
		t.Errorf("status wrong: %+v", st)
	}
}

func TestMonitor_TimeoutRespectedOnSlowEndpoint(t *testing.T) {
	// A real route probe to a never-accepting address should time out fast.
	m := NewMonitor([]HealthCheck{{
		Name: "slow", Kind: KindRoute, Target: "10.255.255.1:9", // non-routable
		Interval: time.Hour, Timeout: 100 * time.Millisecond, Threshold: 1,
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	m.runOnce(ctx, m.checks[0])
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("probe took %v, timeout not respected", elapsed)
	}
	if m.Healthy() {
		t.Error("unreachable target should be unhealthy")
	}
}

func TestMonitor_ConcurrentChecksNoRace(_ *testing.T) {
	checks := make([]HealthCheck, 0, 20)
	seq := map[string][]error{}
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("c%d", i)
		checks = append(checks, HealthCheck{Name: name, Kind: KindRoute, Target: ":1", Interval: 2 * time.Millisecond})
		if i%2 == 0 {
			seq[name] = []error{fmt.Errorf("fail")}
		}
	}
	m := newTestMonitor(seq, checks...)
	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	// Hammer Status/Healthy concurrently while checks run.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = m.Status()
				_ = m.Healthy()
			}
		}()
	}
	// Drain events so the channel doesn't block anything.
	go func() {
		for range m.Events() { //nolint:revive // intentional drain
			_ = struct{}{}
		}
	}()
	wg.Wait()
	cancel()
}
