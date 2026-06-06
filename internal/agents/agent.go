// Package agents implements the VORTEX agent runtime (build plan M10): a
// supervised, message-passing system of autonomous sub-agents coordinated by a
// single user-facing coordinator. This file defines the core types — agent
// state machine, configuration, inter-agent message envelope, the Agent
// interface, and an embeddable BaseAgent that the coordinator and spawned
// sub-agents build upon.
package agents

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Package-level sentinel errors shared by the agent runtime and the message
// bus (defined here so File 1 — agent.go — compiles independently of bus.go).
var (
	// ErrBusFull is returned when a recipient's inbox is full; sends are
	// non-blocking so a slow agent never stalls the sender.
	ErrBusFull = errors.New("agents: recipient inbox full")
	// ErrAgentNotFound is returned when routing to an unregistered agent.
	ErrAgentNotFound = errors.New("agents: agent not found")
	// ErrAlreadyRegistered is returned when registering a duplicate name.
	ErrAlreadyRegistered = errors.New("agents: agent already registered")
)

// AgentState is the lifecycle state of an agent. Agents move through these
// states under the supervision of the runtime; TransitionState enforces the
// legal edges of the state machine.
type AgentState string

const (
	// StateIdle is the initial (and, for persistent agents, the resting) state.
	StateIdle AgentState = "idle"
	// StateRunning means the agent is actively processing work.
	StateRunning AgentState = "running"
	// StateWaiting means the agent is blocked awaiting a message or result.
	StateWaiting AgentState = "waiting"
	// StateError means the agent failed; it may retry back to Idle.
	StateError AgentState = "error"
	// StateComplete means the agent finished its task. Terminal for task
	// agents; persistent agents may return to Idle.
	StateComplete AgentState = "complete"
)

// AgentConfig configures a single agent.
type AgentConfig struct {
	Name        string
	Type        string // "persistent" | "task"
	Description string
	MaxRetries  int           // default 3 (applied by NewBaseAgent if <= 0)
	Timeout     time.Duration // 0 = no timeout
}

// Agent type constants.
const (
	TypePersistent = "persistent"
	TypeTask       = "task"
)

// AgentMessage is the typed envelope passed between agents over the bus.
type AgentMessage struct {
	ID        string
	FromAgent string
	ToAgent   string
	Type      string // "task_brief" | "result" | "error" | "status"
	Payload   any
	Timestamp time.Time
}

// Message type constants.
const (
	MsgTaskBrief = "task_brief"
	MsgResult    = "result"
	MsgError     = "error"
	MsgStatus    = "status"
)

// Agent is the behaviour every agent (coordinator and sub-agents) implements.
type Agent interface {
	Name() string
	State() AgentState
	Start(ctx context.Context) error
	Stop() error
	Send(msg AgentMessage) error
	Receive() <-chan AgentMessage
}

// messageBuffer is the per-agent inbound channel capacity.
const messageBuffer = 100

// BaseAgent is an embeddable implementation of Agent. It owns the state
// machine and the buffered inbound message channel, and provides a supervised
// run wrapper that recovers from panics and drives the agent to StateError.
//
// Embedders typically override the work performed inside Start by supplying a
// run function via NewBaseAgentFunc, or by embedding BaseAgent and providing
// their own Start that calls Supervise.
type BaseAgent struct {
	cfg AgentConfig

	mu    sync.RWMutex
	state AgentState

	inbox chan AgentMessage

	// run is the unit of work executed (and supervised) by Start. It may be
	// nil, in which case Start simply marks the agent Running then Complete.
	run func(ctx context.Context) error
}

// NewBaseAgent constructs a BaseAgent in StateIdle with a buffered inbox.
func NewBaseAgent(cfg AgentConfig) *BaseAgent {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	return &BaseAgent{
		cfg:   cfg,
		state: StateIdle,
		inbox: make(chan AgentMessage, messageBuffer),
	}
}

// NewBaseAgentFunc is like NewBaseAgent but attaches a unit of work run by
// Start under panic supervision.
func NewBaseAgentFunc(cfg AgentConfig, run func(ctx context.Context) error) *BaseAgent {
	a := NewBaseAgent(cfg)
	a.run = run
	return a
}

// Name returns the agent's configured name.
func (a *BaseAgent) Name() string { return a.cfg.Name }

// Config returns a copy of the agent's configuration.
func (a *BaseAgent) Config() AgentConfig { return a.cfg }

// State returns the agent's current lifecycle state.
func (a *BaseAgent) State() AgentState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state
}

// Start drives the agent through its work under panic supervision. It marks the
// agent Running, executes the run function (if any), and transitions to
// Complete on success or Error on failure/panic.
func (a *BaseAgent) Start(ctx context.Context) error {
	if err := a.TransitionState(StateIdle, StateRunning); err != nil {
		return err
	}
	if err := a.Supervise(ctx); err != nil {
		// Supervise already set StateError on panic; ensure it for plain errors.
		if a.State() != StateError {
			_ = a.forceState(StateError)
		}
		return err
	}
	return a.TransitionState(StateRunning, StateComplete)
}

// Supervise runs the agent's work function, recovering from panics and driving
// the agent to StateError if one occurs. It is exported so embedders that
// provide a custom Start can reuse the supervision behaviour.
func (a *BaseAgent) Supervise(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			_ = a.forceState(StateError)
			err = fmt.Errorf("agent %q panicked: %v", a.cfg.Name, r)
		}
	}()
	if a.run == nil {
		return nil
	}
	return a.run(ctx)
}

// Stop transitions the agent back to Idle and is safe to call from any state.
func (a *BaseAgent) Stop() error {
	return a.forceState(StateIdle)
}

// Send places a message on the agent's inbox. It is non-blocking: a full inbox
// returns ErrBusFull rather than blocking the sender.
func (a *BaseAgent) Send(msg AgentMessage) error {
	select {
	case a.inbox <- msg:
		return nil
	default:
		return ErrBusFull
	}
}

// Receive returns the agent's inbound message channel.
func (a *BaseAgent) Receive() <-chan AgentMessage { return a.inbox }

// validTransitions encodes the legal edges of the agent state machine. The
// terminal-ness of Complete depends on the agent type and is handled in
// TransitionState (persistent agents may return Complete → Idle).
var validTransitions = map[AgentState][]AgentState{
	StateIdle:     {StateRunning},
	StateRunning:  {StateWaiting, StateError, StateComplete},
	StateWaiting:  {StateRunning, StateError},
	StateError:    {StateIdle},
	StateComplete: {StateIdle}, // persistent agents only — gated below
}

// TransitionState moves the agent from `from` to `to`, returning an error if
// the current state is not `from` or the edge is illegal. The Complete → Idle
// edge is permitted only for persistent agents (task agents treat Complete as
// terminal).
func (a *BaseAgent) TransitionState(from, to AgentState) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.state != from {
		return fmt.Errorf("agents: cannot transition from %q: agent is in state %q", from, a.state)
	}
	if from == StateComplete && to == StateIdle && a.cfg.Type != TypePersistent {
		return fmt.Errorf("agents: %q is a task agent; Complete is terminal", a.cfg.Name)
	}
	for _, allowed := range validTransitions[from] {
		if allowed == to {
			a.state = to
			return nil
		}
	}
	return fmt.Errorf("agents: invalid transition %q → %q", from, to)
}

// forceState sets the state unconditionally (used by Stop and panic recovery,
// which must not be blocked by the normal transition rules).
func (a *BaseAgent) forceState(to AgentState) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state = to
	return nil
}
