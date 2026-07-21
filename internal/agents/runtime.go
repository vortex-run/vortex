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

// ErrRuntimeStopped is returned by Submit after the runtime has been stopped.
var ErrRuntimeStopped = errors.New("agents: runtime is stopped")

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

	// Lifecycle: ctx is cancelled by Stop so in-flight HandleMessage calls
	// unwind; wg tracks Submit goroutines so Stop can drain them; stopped
	// rejects new Submits once Stop begins.
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	stopped atomic.Bool

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
	ctx, cancel := context.WithCancel(context.Background())
	return &Runtime{
		cfg:       cfg,
		log:       log,
		submitSem: make(chan struct{}, cfg.MaxConcurrent),
		ctx:       ctx,
		cancel:    cancel,
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
	if r.stopped.Load() {
		return nil, ErrRuntimeStopped
	}
	// Register this in-flight Submit under r.mu so wg.Add never races with the
	// wg.Wait in Stop: once Stop has set stopped (also under r.mu), no new Add
	// happens. This closes a TOCTOU gap between the stopped check and wg.Add.
	r.mu.Lock()
	if !r.started || r.stopped.Load() {
		r.mu.Unlock()
		if !r.started {
			return nil, fmt.Errorf("agents: runtime not started")
		}
		return nil, ErrRuntimeStopped
	}

	// Acquire a concurrency slot; reject rather than queue unboundedly.
	select {
	case r.submitSem <- struct{}{}:
	default:
		r.mu.Unlock()
		return nil, ErrTooManyRequests
	}

	out := make(chan string, 8)
	r.queue.Add(1)
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		defer func() { <-r.submitSem }()
		defer close(out)
		defer r.queue.Add(-1)
		r.messages.Add(1)

		// Merge the runtime's lifecycle context with the caller's: the call
		// unwinds if EITHER the caller cancels or Stop cancels the runtime.
		mctx, cancel := context.WithCancel(r.ctx)
		defer cancel()
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-mctx.Done():
			}
		}()

		// Deltas stream into the channel as the coordinator produces them
		// (AGUI item C). The select drops output once the caller is gone, so
		// an abandoned channel can never block this goroutine (the SSE handler
		// stops draining when the client disconnects).
		send := func(chunk string) {
			select {
			case out <- chunk:
			case <-mctx.Done():
			}
		}
		_, err := r.cfg.Coordinator.HandleMessageStream(mctx, userMsg, sessionID, send)
		if err != nil {
			send("error: " + err.Error())
		}
		// On success all content has already been delivered as deltas.
	}()
	return out, nil
}

// Stop rejects new Submits, cancels the runtime context so in-flight
// HandleMessage calls unwind, and waits (up to ctx's deadline) for those
// goroutines to drain before unregistering agents. Sub-agents are unregistered
// first, the coordinator last.
func (r *Runtime) Stop(ctx context.Context) error {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return nil
	}
	r.started = false
	// Set stopped under r.mu so it is ordered with the Submit gate: after this,
	// no new Submit can pass the gate and call wg.Add, so the wg.Wait below
	// cannot race a concurrent wg.Add.
	r.stopped.Store(true)
	r.mu.Unlock()

	// Cancel in-flight work.
	r.cancel()

	// Wait for in-flight Submit goroutines to finish, bounded by ctx.
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	var drainErr error
	select {
	case <-done:
	case <-ctx.Done():
		drainErr = ctx.Err()
		r.log.Warn("agent runtime stop timed out waiting for in-flight work", "err", drainErr)
	}

	active := r.cfg.Coordinator.ActiveAgents()
	for _, name := range active {
		r.cfg.Bus.Unregister(name)
	}
	r.cfg.Bus.Unregister(coordinatorName)
	r.log.Info("agent runtime stopped", "agents_stopped", len(active))
	return drainErr
}

// RuntimeStats is a snapshot of runtime activity.
type RuntimeStats struct {
	ActiveAgents  int
	TotalMessages int64
	QueueDepth    int
	// Memory-tier counts (upgrade brand part 5: the code view's MEMORY panel).
	Skills   int
	Episodes int
	Sessions int
}

// Approve resolves a pending tool-action approval for a session, delegating to
// the coordinator. It returns the result transcript (executed on approval) and
// whether a pending action matched.
func (r *Runtime) Approve(sessionID string, approved bool) (string, bool) {
	return r.cfg.Coordinator.ApproveAction(sessionID, approved)
}

// Coordinator returns the runtime's coordinator (for wiring local-tool calls).
func (r *Runtime) Coordinator() *Coordinator { return r.cfg.Coordinator }

// ListSessions returns stored conversation sessions (newest first).
func (r *Runtime) ListSessions() []SessionInfo { return r.cfg.Coordinator.ListSessions() }

// SessionHistory returns the persisted messages for a session.
func (r *Runtime) SessionHistory(sessionID string) []MemoryMessage {
	return r.cfg.Coordinator.SessionHistory(sessionID)
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
	mem := r.cfg.Coordinator.MemoryStats()
	return RuntimeStats{
		ActiveAgents:  active,
		TotalMessages: r.messages.Load(),
		QueueDepth:    int(r.queue.Load()),
		Skills:        mem.Skills,
		Episodes:      mem.Episodes,
		Sessions:      mem.Sessions,
	}
}
