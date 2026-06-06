package agents

import (
	"context"
	"sync"
	"testing"
	"time"
)

func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	bus := NewBus()
	c, err := NewCoordinator(CoordinatorConfig{
		Bus:       bus,
		AIGateway: StubAIGateway{IntentReply: string(IntentGeneralQuestion), AnswerReply: "ok"},
		MaxAgents: 4,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	rt, err := NewRuntime(RuntimeConfig{
		Bus: bus, Coordinator: c, MaxAgents: 4, SandboxBase: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return rt
}

func TestNewRuntime_RequiresBusAndCoordinator(t *testing.T) {
	if _, err := NewRuntime(RuntimeConfig{Coordinator: &Coordinator{}}); err == nil {
		t.Error("expected error without bus")
	}
	if _, err := NewRuntime(RuntimeConfig{Bus: NewBus()}); err == nil {
		t.Error("expected error without coordinator")
	}
}

func TestRuntime_StartRegistersCoordinator(t *testing.T) {
	rt := newTestRuntime(t)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	names := rt.cfg.Bus.Agents()
	if len(names) != 1 || names[0] != coordinatorName {
		t.Errorf("bus agents = %v, want [coordinator]", names)
	}
}

func TestRuntime_SubmitRequiresStart(t *testing.T) {
	rt := newTestRuntime(t)
	if _, err := rt.Submit(context.Background(), "hi", "s"); err == nil {
		t.Error("Submit before Start should error")
	}
}

func TestRuntime_SubmitReturnsResponseAndCloses(t *testing.T) {
	rt := newTestRuntime(t)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ch, err := rt.Submit(context.Background(), "hello", "s1")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			t.Fatal("channel closed without a response")
		}
		if resp != "ok" {
			t.Errorf("response = %q, want ok", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for response")
	}
	// Channel must close after the single response.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed")
		}
	case <-time.After(time.Second):
		t.Error("channel did not close")
	}
}

func TestRuntime_StopShutsDown(t *testing.T) {
	rt := newTestRuntime(t)
	_ = rt.Start(context.Background())
	_, _ = rt.cfg.Coordinator.SpawnAgent(context.Background(),
		AgentConfig{Name: "w", Type: TypeTask}, nil)
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(rt.cfg.Bus.Agents()) != 0 {
		t.Errorf("bus agents after stop = %v, want empty", rt.cfg.Bus.Agents())
	}
}

func TestRuntime_StatsActiveDecrementsAfterStop(t *testing.T) {
	rt := newTestRuntime(t)
	_ = rt.Start(context.Background())
	_, _ = rt.cfg.Coordinator.SpawnAgent(context.Background(),
		AgentConfig{Name: "w", Type: TypeTask}, nil)
	if got := rt.Stats().ActiveAgents; got != 1 {
		t.Errorf("ActiveAgents before stop = %d, want 1", got)
	}
	_ = rt.Stop(context.Background())
	if got := rt.Stats().ActiveAgents; got != 0 {
		t.Errorf("ActiveAgents after stop = %d, want 0", got)
	}
}

func TestRuntime_ConcurrentSubmit(t *testing.T) {
	rt := newTestRuntime(t)
	_ = rt.Start(context.Background())

	const n = 10
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, err := rt.Submit(context.Background(), "hi", "s")
			if err != nil {
				t.Errorf("Submit: %v", err)
				return
			}
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
				t.Error("submit timed out")
			}
		}()
	}
	wg.Wait()
	if got := rt.Stats().TotalMessages; got != n {
		t.Errorf("TotalMessages = %d, want %d", got, n)
	}
}
