package agents

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestLedger(t *testing.T) *EffectLedger {
	t.Helper()
	l, err := NewEffectLedger(filepath.Join(t.TempDir(), "effects.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestEffectLedger_CommitLookupRoundTrip(t *testing.T) {
	l := newTestLedger(t)
	if _, ok := l.Lookup("run1/t1", "k1"); ok {
		t.Fatal("empty ledger should not find k1")
	}
	if err := l.Commit("run1/t1", "k1", `{"out":"done"}`); err != nil {
		t.Fatal(err)
	}
	got, ok := l.Lookup("run1/t1", "k1")
	if !ok || got != `{"out":"done"}` {
		t.Errorf("Lookup = %q, %v", got, ok)
	}
	// Scope isolation: same key under another scope is absent.
	if _, ok := l.Lookup("run2/t1", "k1"); ok {
		t.Error("scope isolation violated")
	}
}

func TestEffectLedger_CallKeyOccurrences(t *testing.T) {
	l := newTestLedger(t)
	params := map[string]any{"command": "deploy", "arg": 1}
	k1 := l.CallKey("s", "run_command", params)
	k2 := l.CallKey("s", "run_command", params)
	if k1 == k2 {
		t.Errorf("identical calls must get distinct occurrence keys, both = %q", k1)
	}
	// A fresh process (new ledger over the same DB) counts occurrences from 1
	// again — replay order maps first call to first recorded result.
	l2, err := NewEffectLedger(l.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l2.Close() }()
	if k := l2.CallKey("s", "run_command", params); k != k1 {
		t.Errorf("replayed first occurrence key = %q, want %q", k, k1)
	}
	// Different params → different fingerprint.
	if k := l.CallKey("s", "run_command", map[string]any{"command": "other"}); k == k1 {
		t.Error("different params must not collide")
	}
}

func TestEffectLedger_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "effects.db")
	l, err := NewEffectLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Commit("s", "k", "r"); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()

	l2, err := NewEffectLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l2.Close() }()
	if got, ok := l2.Lookup("s", "k"); !ok || got != "r" {
		t.Errorf("after reopen Lookup = %q, %v", got, ok)
	}
}

func TestEffectLedger_ClearScopeAndPrune(t *testing.T) {
	l := newTestLedger(t)
	_ = l.Commit("s1", "k", "r")
	_ = l.Commit("s2", "k", "r")
	if err := l.ClearScope("s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Lookup("s1", "k"); ok {
		t.Error("s1 should be cleared")
	}
	if _, ok := l.Lookup("s2", "k"); !ok {
		t.Error("s2 must survive ClearScope(s1)")
	}
	// Prune with a zero horizon removes everything committed before now.
	time.Sleep(5 * time.Millisecond)
	if err := l.PruneOlderThan(0); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Lookup("s2", "k"); ok {
		t.Error("prune should remove old entries")
	}
}

// countingEffectTool is a SideEffecting test tool that counts executions.
type countingEffectTool struct {
	executions *int
}

func (countingEffectTool) Name() string          { return "effect_tool" }
func (countingEffectTool) Description() string   { return "test side effect" }
func (countingEffectTool) SideEffecting() bool   { return true }
func (c countingEffectTool) Execute(_ context.Context, params map[string]any) (any, error) {
	*c.executions++
	return map[string]any{"ran": *c.executions, "cmd": params["cmd"]}, nil
}

// pureTool is a non-side-effecting test tool that counts executions.
type pureTool struct {
	executions *int
}

func (pureTool) Name() string        { return "pure_tool" }
func (pureTool) Description() string { return "test read" }
func (p pureTool) Execute(_ context.Context, _ map[string]any) (any, error) {
	*p.executions++
	return "read", nil
}

func TestSandboxedRegistry_FencesSideEffects(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "effects.db")
	ledger, err := NewEffectLedger(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()

	execs := 0
	reg := NewToolRegistry()
	_ = reg.Register(countingEffectTool{executions: &execs})
	sandboxed := NewSandboxedRegistry(reg, t.TempDir(), nil, nil).WithEffectLedger(ledger)

	ctx := WithEffectScope(context.Background(), "run1/task1")
	params := map[string]any{"cmd": "deploy"}

	// First attempt executes.
	if _, err := sandboxed.Execute(ctx, "effect_tool", params); err != nil {
		t.Fatal(err)
	}
	if execs != 1 {
		t.Fatalf("executions = %d, want 1", execs)
	}

	// Simulate a crash-resume: fresh process = fresh ledger over the same DB,
	// fresh registry. The replayed identical call must NOT execute again and
	// must return the recorded result.
	ledger2, err := NewEffectLedger(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger2.Close() }()
	sandboxed2 := NewSandboxedRegistry(reg, t.TempDir(), nil, nil).WithEffectLedger(ledger2)

	res, err := sandboxed2.Execute(ctx, "effect_tool", params)
	if err != nil {
		t.Fatal(err)
	}
	if execs != 1 {
		t.Errorf("executions = %d after replay, want 1 (fence must prevent re-execution)", execs)
	}
	m, ok := res.(map[string]any)
	if !ok || m["cmd"] != "deploy" {
		t.Errorf("replayed result = %#v, want the recorded map", res)
	}

	// The resumed attempt's NEXT (new) call executes normally.
	if _, err := sandboxed2.Execute(ctx, "effect_tool", map[string]any{"cmd": "migrate"}); err != nil {
		t.Fatal(err)
	}
	if execs != 2 {
		t.Errorf("executions = %d, want 2 (new tail call must run)", execs)
	}
}

func TestSandboxedRegistry_NoScopeOrPureToolNotFenced(t *testing.T) {
	ledger := newTestLedger(t)
	seExecs, pureExecs := 0, 0
	reg := NewToolRegistry()
	_ = reg.Register(countingEffectTool{executions: &seExecs})
	_ = reg.Register(pureTool{executions: &pureExecs})
	sandboxed := NewSandboxedRegistry(reg, t.TempDir(), nil, nil).WithEffectLedger(ledger)

	// No effect scope: the side-effecting tool executes every time.
	for i := 0; i < 2; i++ {
		if _, err := sandboxed.Execute(context.Background(), "effect_tool", map[string]any{"cmd": "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if seExecs != 2 {
		t.Errorf("unscoped executions = %d, want 2 (no fencing without a scope)", seExecs)
	}

	// Scoped, but a pure tool: never fenced, never journaled.
	ctx := WithEffectScope(context.Background(), "run1/task1")
	for i := 0; i < 2; i++ {
		if _, err := sandboxed.Execute(ctx, "pure_tool", nil); err != nil {
			t.Fatal(err)
		}
	}
	if pureExecs != 2 {
		t.Errorf("pure executions = %d, want 2", pureExecs)
	}
}

func TestSandboxedRegistry_RepeatedIdenticalCallsBothExecuteAndReplay(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "effects.db")
	ledger, _ := NewEffectLedger(dbPath)
	defer func() { _ = ledger.Close() }()

	execs := 0
	reg := NewToolRegistry()
	_ = reg.Register(countingEffectTool{executions: &execs})
	sandboxed := NewSandboxedRegistry(reg, t.TempDir(), nil, nil).WithEffectLedger(ledger)
	ctx := WithEffectScope(context.Background(), "s")
	params := map[string]any{"cmd": "twice"}

	// Two intentional identical calls in one attempt both execute (occurrence
	// keys keep them distinct).
	r1, _ := sandboxed.Execute(ctx, "effect_tool", params)
	r2, _ := sandboxed.Execute(ctx, "effect_tool", params)
	if execs != 2 {
		t.Fatalf("executions = %d, want 2", execs)
	}

	// Replay: both calls return their own recorded results, in order.
	ledger2, _ := NewEffectLedger(dbPath)
	defer func() { _ = ledger2.Close() }()
	sandboxed2 := NewSandboxedRegistry(reg, t.TempDir(), nil, nil).WithEffectLedger(ledger2)
	p1, _ := sandboxed2.Execute(ctx, "effect_tool", params)
	p2, _ := sandboxed2.Execute(ctx, "effect_tool", params)
	if execs != 2 {
		t.Errorf("executions = %d after replay, want still 2", execs)
	}
	if got, want := p1.(map[string]any)["ran"], r1.(map[string]any)["ran"]; got != float64(want.(int)) {
		t.Errorf("first replay ran = %v, want %v", got, want)
	}
	if got, want := p2.(map[string]any)["ran"], r2.(map[string]any)["ran"]; got != float64(want.(int)) {
		t.Errorf("second replay ran = %v, want %v", got, want)
	}
}
