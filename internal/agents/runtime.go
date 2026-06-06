package agents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
)

// ErrTooManyRequests is returned by Submit when the concurrency cap is reached.
var ErrTooManyRequests = errors.New("agents: too many concurrent submissions")

// defaultMaxConcurrent bounds simultaneous in-flight Submits when MaxConcurrent
// is not set.
const defaultMaxConcurrent = 5

// RuntimeConfig configures the persistent agent runtime supervisor.
type RuntimeConfig struct {
	Bus           *Bus
	Coordinator   *Coordinator
	MaxAgents     int
	MaxConcurrent int          // max simultaneous in-flight Submits (default 5)
	SandboxBase   string       // base directory for per-agent sandboxes
	Logger        *slog.Logger // optional; defaults to slog.Default()
}

// Runtime supervises the coordinator and the message bus, exposing a simple
// Submit API for delivering user messages and receiving streamed responses.
type Runtime struct {
	cfg RuntimeConfig
	log *slog.Logger

	mu      sync.Mutex
	started bool

	// submitSem caps concurrent in-flight Submits (DoS guard).
	submitSem chan struct{}

	messages atomic.Int64
	queue    atomic.Int64
}

// NewRuntime constructs a runtime. It requires a Bus and a Coordinator.
func NewRuntime(cfg RuntimeConfig) (*Runtime, error) {
	if cfg.Bus == nil {
		return nil, fmt.Errorf("agents: runtime requires a bus")
	}
	if cfg.Coordinator == nil {
		return nil, fmt.Errorf("agents: runtime requires a coordinator")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = defaultMaxConcurrent
	}
	return &Runtime{
		cfg:       cfg,
		log:       log,
		submitSem: make(chan struct{}, cfg.MaxConcurrent),
	}, nil
}

// Start registers the coordinator on the bus, ensures the sandbox base exists,
// and marks the runtime ready. It is idempotent.
func (r *Runtime) Start(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return nil
	}
	if r.cfg.SandboxBase != "" {
		if err := os.MkdirAll(r.cfg.SandboxBase, 0o700); err != nil {
			return fmt.Errorf("agents: create sandbox base: %w", err)
		}
	}
	if err := r.cfg.Bus.Register(r.cfg.Coordinator); err != nil && !errors.Is(err, ErrAlreadyRegistered) {
		return err
	}
	r.started = true
	r.log.Info("agent runtime started",
		"max_agents", r.cfg.MaxAgents, "sandbox_base", r.cfg.SandboxBase)
	return nil
}

// Submit delivers userMsg to the coordinator and returns a channel that
// receives the response (then closes). Processing is asynchronous; the call
// itself does not block on the coordinator.
func (r *Runtime) Submit(ctx context.Context, userMsg, sessionID string) (<-chan string, error) {
	r.mu.Lock()
	started := r.started
	r.mu.Unlock()
	if !started {
		return nil, fmt.Errorf("agents: runtime not started")
	}

	// Acquire a concurrency slot; reject rather than queue unboundedly.
	select {
	case r.submitSem <- struct{}{}:
	default:
		return nil, ErrTooManyRequests
	}

	out := make(chan string, 1)
	r.queue.Add(1)
	go func() {
		defer func() { <-r.submitSem }()
		defer close(out)
		defer r.queue.Add(-1)
		r.messages.Add(1)
		resp, err := r.cfg.Coordinator.HandleMessage(ctx, userMsg, sessionID)
		if err != nil {
			out <- "error: " + err.Error()
			return
		}
		out <- resp
	}()
	return out, nil
}

// Stop gracefully stops all active sub-agents (coordinator last) and marks the
// runtime stopped.
func (r *Runtime) Stop(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		return nil
	}
	active := r.cfg.Coordinator.ActiveAgents()
	for _, name := range active {
		r.cfg.Bus.Unregister(name)
	}
	r.cfg.Bus.Unregister(coordinatorName)
	r.started = false
	r.log.Info("agent runtime stopped", "agents_stopped", len(active))
	return nil
}

// RuntimeStats is a snapshot of runtime activity.
type RuntimeStats struct {
	ActiveAgents  int
	TotalMessages int64
	QueueDepth    int
}

// Stats returns a snapshot of runtime activity.
func (r *Runtime) Stats() RuntimeStats {
	r.mu.Lock()
	started := r.started
	r.mu.Unlock()
	active := 0
	if started {
		active = len(r.cfg.Coordinator.ActiveAgents())
	}
	return RuntimeStats{
		ActiveAgents:  active,
		TotalMessages: r.messages.Load(),
		QueueDepth:    int(r.queue.Load()),
	}
}
