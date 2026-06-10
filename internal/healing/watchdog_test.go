package healing

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// testWatchdog builds a watchdog with fast intervals and injected hooks.
func testWatchdog(cfg WatchdogConfig) *Watchdog {
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 5 * time.Millisecond
	}
	if cfg.RestartDelay == 0 {
		cfg.RestartDelay = time.Millisecond
	}
	return NewWatchdog(cfg)
}

func TestWatchdog_DetectsMissingPidfileAsDown(t *testing.T) {
	// A real liveness check against a non-existent pidfile must be "down".
	if pidfileAlive(filepath.Join(t.TempDir(), "nope.pid")) {
		t.Error("missing pidfile should report not alive")
	}
}

func TestWatchdog_RestartsWhenDown(t *testing.T) {
	var spawned atomic.Int32
	w := testWatchdog(WatchdogConfig{
		MaxRestarts: 5,
		isAlive:     func(string) bool { return false }, // always down
		spawn:       func(context.Context) error { spawned.Add(1); return nil },
		waitUp:      func(context.Context, string) bool { return true },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = w.Watch(ctx)
	if spawned.Load() == 0 {
		t.Error("watchdog should have attempted at least one restart")
	}
	if w.RestartCount() == 0 || w.LastRestart().IsZero() {
		t.Errorf("RestartCount=%d LastRestart=%v should be set", w.RestartCount(), w.LastRestart())
	}
}

func TestWatchdog_RespectsRestartDelay(t *testing.T) {
	var firstSpawn atomic.Int64
	start := time.Now()
	w := NewWatchdog(WatchdogConfig{
		CheckInterval: time.Millisecond,
		RestartDelay:  60 * time.Millisecond,
		MaxRestarts:   5,
		isAlive:       func(string) bool { return false },
		spawn: func(context.Context) error {
			firstSpawn.CompareAndSwap(0, time.Since(start).Milliseconds())
			return nil
		},
		waitUp: func(context.Context, string) bool { return true },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = w.Watch(ctx)
	if d := firstSpawn.Load(); d < 50 {
		t.Errorf("first restart happened after %dms, want >= ~60ms (RestartDelay)", d)
	}
}

func TestWatchdog_RespectsMaxRestarts(t *testing.T) {
	var spawned atomic.Int32
	w := NewWatchdog(WatchdogConfig{
		CheckInterval: time.Millisecond,
		RestartDelay:  time.Millisecond,
		MaxRestarts:   2,
		isAlive:       func(string) bool { return false },
		spawn:         func(context.Context) error { spawned.Add(1); return nil },
		waitUp:        func(context.Context, string) bool { return true },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = w.Watch(ctx)
	if got := spawned.Load(); got != 2 {
		t.Errorf("MaxRestarts=2 should cap spawns at 2, got %d", got)
	}
}

func TestWatchdog_RestartCountIncrements(t *testing.T) {
	w := NewWatchdog(WatchdogConfig{
		CheckInterval: time.Millisecond,
		RestartDelay:  time.Millisecond,
		MaxRestarts:   3,
		isAlive:       func(string) bool { return false },
		spawn:         func(context.Context) error { return nil },
		waitUp:        func(context.Context, string) bool { return true },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = w.Watch(ctx)
	if w.RestartCount() != 3 {
		t.Errorf("RestartCount = %d, want 3 (capped by MaxRestarts)", w.RestartCount())
	}
}

func TestWatchdog_ExitsOnContextCancel(t *testing.T) {
	w := NewWatchdog(WatchdogConfig{
		CheckInterval: 5 * time.Millisecond,
		isAlive:       func(string) bool { return true }, // healthy → never restarts
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Watch(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Watch should return ctx.Err() on cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("Watch did not exit on context cancel")
	}
	if w.RestartCount() != 0 {
		t.Error("a healthy process should never be restarted")
	}
}

func TestWatchdog_PidfileAliveForRealProcess(t *testing.T) {
	// Write our own PID to a pidfile; the watchdog should see it as alive.
	dir := t.TempDir()
	pf := filepath.Join(dir, "vortex.pid")
	if err := os.WriteFile(pf, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if !pidfileAlive(pf) {
		t.Error("current process pidfile should be alive")
	}
}
