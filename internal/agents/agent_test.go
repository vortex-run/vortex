package agents

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNewBaseAgent_StartsIdle(t *testing.T) {
	a := NewBaseAgent(AgentConfig{Name: "a", Type: TypeTask})
	if a.State() != StateIdle {
		t.Errorf("new agent state = %q, want %q", a.State(), StateIdle)
	}
	if a.Name() != "a" {
		t.Errorf("Name = %q, want a", a.Name())
	}
	if a.Config().MaxRetries != 3 {
		t.Errorf("MaxRetries default = %d, want 3", a.Config().MaxRetries)
	}
}

func TestTransitionState_ValidEdges(t *testing.T) {
	a := NewBaseAgent(AgentConfig{Name: "a", Type: TypeTask})
	steps := []struct{ from, to AgentState }{
		{StateIdle, StateRunning},
		{StateRunning, StateWaiting},
		{StateWaiting, StateRunning},
		{StateRunning, StateComplete},
	}
	for _, s := range steps {
		if err := a.TransitionState(s.from, s.to); err != nil {
			t.Fatalf("transition %q→%q: unexpected error %v", s.from, s.to, err)
		}
		if a.State() != s.to {
			t.Fatalf("state = %q, want %q", a.State(), s.to)
		}
	}
}

func TestTransitionState_InvalidEdgeErrors(t *testing.T) {
	a := NewBaseAgent(AgentConfig{Name: "a", Type: TypeTask})
	// Idle → Complete is not a legal edge.
	if err := a.TransitionState(StateIdle, StateComplete); err == nil {
		t.Error("expected error on Idle→Complete, got nil")
	}
	// Wrong `from` (agent is Idle, not Running).
	if err := a.TransitionState(StateRunning, StateComplete); err == nil {
		t.Error("expected error when from != current state, got nil")
	}
}

func TestSend_PutsMessageInChannel(t *testing.T) {
	a := NewBaseAgent(AgentConfig{Name: "a", Type: TypeTask})
	msg := AgentMessage{ID: "1", ToAgent: "a", Type: MsgStatus}
	if err := a.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case got := <-a.Receive():
		if got.ID != "1" {
			t.Errorf("received ID = %q, want 1", got.ID)
		}
	default:
		t.Error("expected a message on the inbox")
	}
}

func TestSend_FullInboxReturnsErrBusFull(t *testing.T) {
	a := NewBaseAgent(AgentConfig{Name: "a", Type: TypeTask})
	for i := 0; i < messageBuffer; i++ {
		if err := a.Send(AgentMessage{ID: "x"}); err != nil {
			t.Fatalf("fill send %d: %v", i, err)
		}
	}
	if err := a.Send(AgentMessage{ID: "overflow"}); !errors.Is(err, ErrBusFull) {
		t.Errorf("overflow send err = %v, want ErrBusFull", err)
	}
}

func TestStart_PanicRecoveredToError(t *testing.T) {
	a := NewBaseAgentFunc(AgentConfig{Name: "boom", Type: TypeTask}, func(context.Context) error {
		panic("kaboom")
	})
	err := a.Start(context.Background())
	if err == nil {
		t.Fatal("expected error from panicking agent")
	}
	if a.State() != StateError {
		t.Errorf("state after panic = %q, want %q", a.State(), StateError)
	}
}

func TestStart_SuccessReachesComplete(t *testing.T) {
	var ran bool
	a := NewBaseAgentFunc(AgentConfig{Name: "ok", Type: TypeTask}, func(context.Context) error {
		ran = true
		return nil
	})
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !ran {
		t.Error("run function was not invoked")
	}
	if a.State() != StateComplete {
		t.Errorf("state = %q, want %q", a.State(), StateComplete)
	}
}

func TestPersistentAgent_CompleteToIdleAllowed(t *testing.T) {
	a := NewBaseAgent(AgentConfig{Name: "p", Type: TypePersistent})
	mustTransition(t, a, StateIdle, StateRunning)
	mustTransition(t, a, StateRunning, StateComplete)
	if err := a.TransitionState(StateComplete, StateIdle); err != nil {
		t.Errorf("persistent Complete→Idle should be allowed: %v", err)
	}
}

func TestTaskAgent_CompleteIsTerminal(t *testing.T) {
	a := NewBaseAgent(AgentConfig{Name: "t", Type: TypeTask})
	mustTransition(t, a, StateIdle, StateRunning)
	mustTransition(t, a, StateRunning, StateComplete)
	if err := a.TransitionState(StateComplete, StateIdle); err == nil {
		t.Error("task agent Complete→Idle should be rejected")
	}
}

func TestErrorState_RetriesToIdle(t *testing.T) {
	a := NewBaseAgent(AgentConfig{Name: "r", Type: TypeTask, Timeout: time.Second})
	mustTransition(t, a, StateIdle, StateRunning)
	mustTransition(t, a, StateRunning, StateError)
	if err := a.TransitionState(StateError, StateIdle); err != nil {
		t.Errorf("Error→Idle (retry) should be allowed: %v", err)
	}
}

func mustTransition(t *testing.T, a *BaseAgent, from, to AgentState) {
	t.Helper()
	if err := a.TransitionState(from, to); err != nil {
		t.Fatalf("transition %q→%q: %v", from, to, err)
	}
}
