// Package lifecycle manages VORTEX's process lifecycle: ordered startup, signal
// handling, and graceful shutdown (build plan M1.4).
//
// Two POSIX signals are meaningful to VORTEX:
//
//   - SIGTERM (and SIGINT): begin a graceful shutdown. Registered shutdown
//     hooks run in reverse registration order — last-registered first — so
//     dependencies are torn down before the things they depend on.
//   - SIGHUP: request a configuration hot-reload (vortex.cue is re-validated and
//     re-applied without dropping connections). Registered reload hooks fire.
//
// On Windows, where SIGHUP and SIGTERM do not exist, only os.Interrupt
// (Ctrl+C) is wired up; the reload path can still be driven programmatically
// via Reload. This keeps the package buildable and testable on every platform
// the binary targets while behaving correctly on Linux servers in production.
package lifecycle

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"time"
)

// Hook is a unit of shutdown or reload work. The context carries the deadline
// for the operation; hooks should respect it and return promptly.
type Hook func(ctx context.Context) error

// Manager coordinates signal handling and graceful shutdown. The zero value is
// not ready for use; construct one with New.
type Manager struct {
	log     *slog.Logger
	timeout time.Duration

	mu        sync.Mutex
	shutdowns []namedHook
	reloads   []namedHook

	done chan struct{} // closed once Run returns
}

type namedHook struct {
	name string
	fn   Hook
}

// Config configures a Manager.
type Config struct {
	// Logger receives lifecycle events. Required.
	Logger *slog.Logger
	// ShutdownTimeout bounds how long all shutdown hooks together may take.
	// Defaults to 30s if zero.
	ShutdownTimeout time.Duration
}

// New constructs a Manager.
func New(cfg Config) *Manager {
	timeout := cfg.ShutdownTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Manager{
		log:     cfg.Logger,
		timeout: timeout,
		done:    make(chan struct{}),
	}
}

// OnShutdown registers a hook to run during graceful shutdown. Hooks run in
// reverse registration order. name is used in logs.
func (m *Manager) OnShutdown(name string, fn Hook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shutdowns = append(m.shutdowns, namedHook{name: name, fn: fn})
}

// OnReload registers a hook to run when a SIGHUP (or Reload call) requests a
// configuration hot-reload. Hooks run in registration order.
func (m *Manager) OnReload(name string, fn Hook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reloads = append(m.reloads, namedHook{name: name, fn: fn})
}

// Run blocks until a shutdown signal arrives or the supplied context is
// cancelled, handling reload signals along the way. It then runs all shutdown
// hooks and returns. Run is intended to be the last call in main.
func (m *Manager) Run(ctx context.Context) {
	defer close(m.done)

	sigCh := make(chan os.Signal, 1)
	notifyShutdown(sigCh)
	notifyReload(sigCh)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-ctx.Done():
			m.log.Info("context cancelled, shutting down")
			m.runShutdown()
			return
		case sig := <-sigCh:
			if isReload(sig) {
				m.log.Info("reload signal received", "signal", sig.String())
				m.runReload()
				continue
			}
			m.log.Info("shutdown signal received", "signal", sig.String())
			m.runShutdown()
			return
		}
	}
}

// Reload triggers the reload hooks programmatically (used by tests and by the
// CLI `vortex reload` path on platforms without SIGHUP).
func (m *Manager) Reload() {
	m.runReload()
}

// Shutdown triggers the shutdown hooks programmatically without waiting for a
// signal. Safe to call at most once.
func (m *Manager) Shutdown() {
	m.runShutdown()
}

// Done returns a channel closed once Run has finished its shutdown sequence.
func (m *Manager) Done() <-chan struct{} {
	return m.done
}

func (m *Manager) runShutdown() {
	m.mu.Lock()
	hooks := make([]namedHook, len(m.shutdowns))
	copy(hooks, m.shutdowns)
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()

	// Reverse order: last registered tears down first.
	for i := len(hooks) - 1; i >= 0; i-- {
		h := hooks[i]
		start := time.Now()
		if err := h.fn(ctx); err != nil {
			m.log.Error("shutdown hook failed", "hook", h.name, "err", err)
		} else {
			m.log.Info("shutdown hook done", "hook", h.name, "took", time.Since(start).String())
		}
		if ctx.Err() != nil {
			m.log.Error("shutdown deadline exceeded; remaining hooks skipped", "timeout", m.timeout.String())
			return
		}
	}
}

func (m *Manager) runReload() {
	m.mu.Lock()
	hooks := make([]namedHook, len(m.reloads))
	copy(hooks, m.reloads)
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()

	for _, h := range hooks {
		if err := h.fn(ctx); err != nil {
			m.log.Error("reload hook failed", "hook", h.name, "err", err)
		} else {
			m.log.Info("reload hook done", "hook", h.name)
		}
	}
}
