package observability

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestProfiler_DisabledIsNoop(t *testing.T) {
	p := NewProfiler(ProfilerConfig{Enabled: false})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled; Start must return immediately for a disabled profiler
	if err := p.Start(ctx); err != nil {
		t.Errorf("disabled profiler Start = %v, want nil", err)
	}
	if p.Addr() != "" {
		t.Error("disabled profiler should not bind an address")
	}
}

func TestProfiler_EnabledServesPprofIndex(t *testing.T) {
	p := NewProfiler(ProfilerConfig{Enabled: true, Endpoint: "127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Start(ctx) }()

	addr := waitProfilerAddr(t, p)
	resp, err := http.Get("http://" + addr + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET pprof index: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("pprof index status = %d, want 200", resp.StatusCode)
	}
}

func TestProfiler_RejectsNonLoopback(t *testing.T) {
	p := NewProfiler(ProfilerConfig{Enabled: true, Endpoint: "0.0.0.0:0"})
	if err := p.Start(context.Background()); err == nil {
		t.Error("profiler should reject a non-loopback endpoint")
	}
}

func TestProfiler_StopCleansUp(t *testing.T) {
	p := NewProfiler(ProfilerConfig{Enabled: true, Endpoint: "127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = p.Start(ctx) }()
	_ = waitProfilerAddr(t, p)

	cancel() // triggers graceful shutdown inside Start
	// Stop again explicitly must be safe.
	if err := p.Stop(); err != nil {
		t.Errorf("Stop = %v, want nil", err)
	}
}

// waitProfilerAddr waits until the profiler has bound its address.
func waitProfilerAddr(t *testing.T, p *Profiler) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a := p.Addr(); a != "" {
			return a
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("profiler never bound an address")
	return ""
}
