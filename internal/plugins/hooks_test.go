package plugins

import (
	"context"
	"errors"
	"testing"
)

// mockHook is a configurable test Hook.
type mockHook struct {
	name  string
	out   HookOutput
	err   error
	calls *[]string // optional call-order recorder
}

func (m *mockHook) Name() string   { return m.name }
func (m *mockHook) Type() HookType { return HookPreRequest }
func (m *mockHook) Execute(_ context.Context, _ HookInput) (HookOutput, error) {
	if m.calls != nil {
		*m.calls = append(*m.calls, m.name)
	}
	return m.out, m.err
}

func TestHookChain_SingleHook(t *testing.T) {
	c := NewHookChain(false)
	c.Register(&mockHook{name: "a", out: HookOutput{Allow: true}}, 0)
	out, err := c.Execute(context.Background(), HookInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Allow {
		t.Error("single allowing hook should allow")
	}
}

func TestHookChain_DenyStopsChain(t *testing.T) {
	var calls []string
	c := NewHookChain(false)
	c.Register(&mockHook{name: "first", out: HookOutput{Allow: false}, calls: &calls}, 0)
	c.Register(&mockHook{name: "second", out: HookOutput{Allow: true}, calls: &calls}, 0)

	out, err := c.Execute(context.Background(), HookInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Allow {
		t.Error("a denying hook should make the chain deny")
	}
	if len(calls) != 1 || calls[0] != "first" {
		t.Errorf("chain should stop after the deny; calls = %v", calls)
	}
}

func TestHookChain_HeaderMerge(t *testing.T) {
	c := NewHookChain(false)
	c.Register(&mockHook{name: "a", out: HookOutput{Allow: true, Headers: map[string][]string{"X-A": {"1"}}}}, 0)
	c.Register(&mockHook{name: "b", out: HookOutput{Allow: true, Headers: map[string][]string{"X-B": {"2"}}}}, 0)

	out, err := c.Execute(context.Background(), HookInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Modified {
		t.Error("output should be marked Modified")
	}
	if out.Headers["X-A"][0] != "1" || out.Headers["X-B"][0] != "2" {
		t.Errorf("headers not merged from both hooks: %v", out.Headers)
	}
}

func TestHookChain_PriorityOrdering(t *testing.T) {
	var calls []string
	c := NewHookChain(true) // priority ordering on
	// Register out of order; lower priority must run first.
	c.Register(&mockHook{name: "high", out: HookOutput{Allow: true}, calls: &calls}, 100)
	c.Register(&mockHook{name: "low", out: HookOutput{Allow: true}, calls: &calls}, 1)
	c.Register(&mockHook{name: "mid", out: HookOutput{Allow: true}, calls: &calls}, 50)

	if _, err := c.Execute(context.Background(), HookInput{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"low", "mid", "high"}
	for i, w := range want {
		if calls[i] != w {
			t.Errorf("execution order = %v, want %v", calls, want)
			break
		}
	}
}

func TestHookChain_FirstNonZeroStatusWins(t *testing.T) {
	c := NewHookChain(false)
	c.Register(&mockHook{name: "a", out: HookOutput{Allow: true}}, 0)              // no override
	c.Register(&mockHook{name: "b", out: HookOutput{Allow: true, Status: 418}}, 0) // first override
	c.Register(&mockHook{name: "c", out: HookOutput{Allow: true, Status: 500}}, 0) // ignored

	out, err := c.Execute(context.Background(), HookInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != 418 {
		t.Errorf("Status = %d, want 418 (first non-zero override)", out.Status)
	}
}

func TestHookChain_ErrorPropagates(t *testing.T) {
	c := NewHookChain(false)
	sentinel := errors.New("boom")
	c.Register(&mockHook{name: "a", err: sentinel}, 0)
	c.Register(&mockHook{name: "b", out: HookOutput{Allow: true}}, 0)

	out, err := c.Execute(context.Background(), HookInput{})
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want sentinel propagated", err)
	}
	if out.Allow {
		t.Error("on hook error the chain should not allow")
	}
}
