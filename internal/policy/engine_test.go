package policy

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// writePolicy writes a single .rego file into a fresh temp dir and returns the
// dir path.
func writePolicy(t *testing.T, rego string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.rego"), []byte(rego), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestNewEngine_EmptyDirLoadsDefault(t *testing.T) {
	e, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !e.UsingDefault() {
		t.Error("empty PolicyDir should use the built-in default policy")
	}
}

func TestDefaultPolicy_AllowsAnyInput(t *testing.T) {
	e, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []map[string]any{
		nil,
		{"method": "GET"},
		{"method": "DELETE", "path": "/admin", "blocked": true},
	} {
		ok, err := e.Eval(context.Background(), in)
		if err != nil {
			t.Fatalf("Eval(%v) error: %v", in, err)
		}
		if !ok {
			t.Errorf("default policy should allow %v", in)
		}
	}
}

func TestNewEngine_LoadsRegoFile(t *testing.T) {
	dir := writePolicy(t, `package vortex

default allow = true
`)
	e, err := NewEngine(EngineConfig{PolicyDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if e.UsingDefault() {
		t.Error("a loaded .rego file should not be the built-in default")
	}
	ok, err := e.Eval(context.Background(), map[string]any{"x": 1})
	if err != nil || !ok {
		t.Errorf("Eval = %v, %v; want true, nil", ok, err)
	}
}

func TestCustomPolicy_EvaluatesCorrectly(t *testing.T) {
	// Deny when input.blocked is true, otherwise allow.
	dir := writePolicy(t, `package vortex

default allow = false

allow if {
	input.blocked == false
}
`)
	e, err := NewEngine(EngineConfig{PolicyDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := e.Eval(context.Background(), map[string]any{"blocked": false})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("blocked=false should be allowed")
	}
	ok, err = e.Eval(context.Background(), map[string]any{"blocked": true})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("blocked=true should be denied")
	}
}

func TestNewEngine_InvalidRegoErrors(t *testing.T) {
	dir := writePolicy(t, "this is not valid rego !!!")
	if _, err := NewEngine(EngineConfig{PolicyDir: dir}); err == nil {
		t.Error("expected compile error for invalid .rego file")
	}
}

func TestReload_SwapsPolicyAtomically(t *testing.T) {
	dir := writePolicy(t, `package vortex

default allow = true
`)
	e, err := NewEngine(EngineConfig{PolicyDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ok, _ := e.Eval(context.Background(), nil)
	if !ok {
		t.Fatal("initial allow-all should permit")
	}

	// Replace with deny-all and reload.
	if werr := os.WriteFile(filepath.Join(dir, "policy.rego"), []byte(`package vortex

default allow = false
`), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if rerr := e.Reload(context.Background()); rerr != nil {
		t.Fatal(rerr)
	}
	ok, err = e.Eval(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("after reloading deny-all, Eval should be false")
	}
}

func TestReload_KeepsOldPolicyOnError(t *testing.T) {
	dir := writePolicy(t, `package vortex

default allow = false

allow if {
	input.ok == true
}
`)
	e, err := NewEngine(EngineConfig{PolicyDir: dir})
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt the policy file, then reload — it must fail and keep the old one.
	if werr := os.WriteFile(filepath.Join(dir, "policy.rego"), []byte("garbage ]["), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if rerr := e.Reload(context.Background()); rerr == nil {
		t.Fatal("Reload with invalid rego should return an error")
	}

	// Old policy still in force: ok=true allowed, ok=false denied.
	ok, err := e.Eval(context.Background(), map[string]any{"ok": true})
	if err != nil || !ok {
		t.Errorf("old policy should still allow ok=true: %v, %v", ok, err)
	}
	ok, _ = e.Eval(context.Background(), map[string]any{"ok": false})
	if ok {
		t.Error("old policy should still deny ok=false")
	}
}

func TestEval_ConcurrentNoRace(t *testing.T) {
	dir := writePolicy(t, `package vortex

default allow = false

allow if {
	input.n > 0
}
`)
	e, err := NewEngine(EngineConfig{PolicyDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, eerr := e.Eval(context.Background(), map[string]any{"n": n}); eerr != nil {
				t.Errorf("concurrent Eval error: %v", eerr)
			}
		}(i)
	}
	wg.Wait()
}
