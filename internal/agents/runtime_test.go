package agents

import (
	"context"
	"errors"
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
		Bus: bus, Coordinator: c, MaxAgents: 4,
		// Generous concurrency so tests that fire many simultaneous Submits
		// exercise contention without tripping the DoS cap (the cap itself is
		// covered by the API-layer 503 test and TestRuntime_ConcurrencyCap).
		MaxConcurrent: 64,
		SandboxBase:   t.TempDir(),
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

func TestRuntime_ConcurrencyCap(t *testing.T) {
	// MaxConcurrent=1 with a handler that blocks until released: the first
	// Submit holds the only slot, so a second concurrent Submit is rejected
	// with ErrTooManyRequests.
	release := make(chan struct{})
	bus := NewBus()
	c, err := NewCoordinator(CoordinatorConfig{
		Bus:       bus,
		AIGateway: blockingGateway{release: release},
		MaxAgents: 4,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	rt, err := NewRuntime(RuntimeConfig{
		Bus: bus, Coordinator: c, MaxConcurrent: 1, SandboxBase: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	_ = rt.Start(context.Background())
	defer func() { close(release); _ = rt.Stop(context.Background()) }()

	ch1, err := rt.Submit(context.Background(), "q", "s")
	if err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	_ = ch1
	// Give the goroutine a moment to acquire the single slot.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if rt.Stats().QueueDepth == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := rt.Submit(context.Background(), "q2", "s"); !errors.Is(err, ErrTooManyRequests) {
		t.Errorf("second Submit err = %v, want ErrTooManyRequests", err)
	}
}

// slowGateway is an AIGateway whose Complete blocks for delay or until the
// context is cancelled. It is used to verify Stop cancels and drains in-flight
// work promptly.
type slowGateway struct{ delay time.Duration }

func (g slowGateway) Complete(ctx context.Context, _, _ string) (string, error) {
	select {
	case <-time.After(g.delay):
		return "done", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// blockingGateway blocks Complete until release is closed (or ctx is done),
// used to hold a concurrency slot open while testing the cap.
type blockingGateway struct{ release chan struct{} }

func (g blockingGateway) Complete(ctx context.Context, _, _ string) (string, error) {
	select {
	case <-g.release:
		return "ok", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// stubbornGateway ignores context cancellation entirely, blocking for the full
// delay — used to force Stop's drain to hit its deadline.
type stubbornGateway struct {
	delay   time.Duration
	started chan struct{}
}

func (g stubbornGateway) Complete(_ context.Context, _, _ string) (string, error) {
	if g.started != nil {
		close(g.started)
	}
	time.Sleep(g.delay) // deliberately ignores ctx
	return "done", nil
}

func newRuntimeWithGateway(t *testing.T, gw AIGateway) *Runtime {
	t.Helper()
	bus := NewBus()
	c, err := NewCoordinator(CoordinatorConfig{Bus: bus, AIGateway: gw, MaxAgents: 4})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	rt, err := NewRuntime(RuntimeConfig{Bus: bus, Coordinator: c, SandboxBase: t.TempDir()})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return rt
}

func TestRuntime_SubmitAfterStopRejected(t *testing.T) {
	rt := newTestRuntime(t)
	_ = rt.Start(context.Background())
	_ = rt.Stop(context.Background())
	if _, err := rt.Submit(context.Background(), "hi", "s"); !errors.Is(err, ErrRuntimeStopped) {
		t.Errorf("Submit after Stop err = %v, want ErrRuntimeStopped", err)
	}
}

func TestRuntime_StopDrainsInFlight(t *testing.T) {
	// A cancellable slow handler: Stop cancels it, and the drain must wait for
	// the goroutine to actually finish (channel closed) before returning.
	rt := newRuntimeWithGateway(t, slowGateway{delay: 2 * time.Second})
	_ = rt.Start(context.Background())

	ch, err := rt.Submit(context.Background(), "general question", "s")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if serr := rt.Stop(stopCtx); serr != nil {
		t.Fatalf("Stop should drain cleanly (cancellable handler), got: %v", serr)
	}
	// After Stop returns, the in-flight goroutine has finished: the channel is
	// closed and readable without blocking.
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Error("response channel not closed — goroutine was not drained")
	}
	// QueueDepth must be back to zero (goroutine fully unwound).
	if d := rt.Stats().QueueDepth; d != 0 {
		t.Errorf("QueueDepth after drain = %d, want 0", d)
	}
}

func TestRuntime_StopTimesOut(t *testing.T) {
	// A handler that ignores cancellation blocks ~2s; Stop with a 100ms
	// deadline must give up waiting and return context.DeadlineExceeded.
	started := make(chan struct{})
	rt := newRuntimeWithGateway(t, stubbornGateway{delay: 2 * time.Second, started: started})
	_ = rt.Start(context.Background())

	if _, err := rt.Submit(context.Background(), "general question", "s"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-started // ensure the handler is actually running before we Stop

	stopCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := rt.Stop(stopCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Stop timeout err = %v, want context.DeadlineExceeded", err)
	}
}

func TestRuntime_ConcurrentSubmitAndStop(t *testing.T) {
	rt := newRuntimeWithGateway(t, slowGateway{delay: 20 * time.Millisecond})
	_ = rt.Start(context.Background())

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ch, err := rt.Submit(context.Background(), "q", "s"); err == nil {
				<-ch
			}
		}()
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = rt.Stop(stopCtx)
	wg.Wait()
}
