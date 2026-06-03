package lifecycle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestShutdownHooksRunInReverseOrder(t *testing.T) {
	m := New(Config{Logger: testLogger()})

	var mu sync.Mutex
	var order []string
	mk := func(name string) Hook {
		return func(context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}
	m.OnShutdown("first", mk("first"))
	m.OnShutdown("second", mk("second"))
	m.OnShutdown("third", mk("third"))

	m.Shutdown()

	want := []string{"third", "second", "first"}
	if len(order) != len(want) {
		t.Fatalf("ran %d hooks, want %d: %v", len(order), len(want), order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q (full: %v)", i, order[i], want[i], order)
		}
	}
}

func TestReloadHooksRunInOrder(t *testing.T) {
	m := New(Config{Logger: testLogger()})
	var order []string
	m.OnReload("a", func(context.Context) error { order = append(order, "a"); return nil })
	m.OnReload("b", func(context.Context) error { order = append(order, "b"); return nil })

	m.Reload()

	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Errorf("reload order = %v, want [a b]", order)
	}
}

func TestShutdownContinuesAfterHookError(t *testing.T) {
	m := New(Config{Logger: testLogger()})
	ran := false
	m.OnShutdown("ok", func(context.Context) error { ran = true; return nil })
	m.OnShutdown("bad", func(context.Context) error { return errors.New("boom") })

	m.Shutdown()

	if !ran {
		t.Error("a failing hook must not prevent earlier-registered hooks from running")
	}
}

func TestRunReturnsOnContextCancel(t *testing.T) {
	m := New(Config{Logger: testLogger()})
	cleaned := false
	m.OnShutdown("cleanup", func(context.Context) error { cleaned = true; return nil })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
	if !cleaned {
		t.Error("shutdown hook should run when context is cancelled")
	}

	select {
	case <-m.Done():
	default:
		t.Error("Done() channel should be closed after Run returns")
	}
}

func TestDefaultTimeoutApplied(t *testing.T) {
	m := New(Config{Logger: testLogger()})
	if m.timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want 30s", m.timeout)
	}
}
