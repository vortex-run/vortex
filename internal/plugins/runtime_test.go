package plugins

import (
	"context"
	"testing"
	"time"
)

// addWASM is a minimal valid WebAssembly module that exports:
//   - memory "memory" (1 page)
//   - func "add" (i32, i32) -> i32 returning a+b
//
// It is hand-encoded so tests need no external WASM toolchain.
var addWASM = []byte{
	0x00, 0x61, 0x73, 0x6d, // magic "\0asm"
	0x01, 0x00, 0x00, 0x00, // version 1

	// Type section: one type (i32,i32)->i32
	0x01, 0x07, 0x01, 0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7f,

	// Function section: one function of type 0
	0x03, 0x02, 0x01, 0x00,

	// Memory section: one memory, min 1 page
	0x05, 0x03, 0x01, 0x00, 0x01,

	// Export section: "memory" (mem 0) and "add" (func 0); payload is 16 bytes
	0x07, 0x10, 0x02,
	0x06, 0x6d, 0x65, 0x6d, 0x6f, 0x72, 0x79, 0x02, 0x00, // "memory" -> mem 0
	0x03, 0x61, 0x64, 0x64, 0x00, 0x00, // "add" -> func 0

	// Code section: func body: local.get 0; local.get 1; i32.add; end
	0x0a, 0x09, 0x01, 0x07, 0x00, 0x20, 0x00, 0x20, 0x01, 0x6a, 0x0b,
}

// badWASM is not a valid module (wrong magic).
var badWASM = []byte{0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00}

func newRuntime(t *testing.T) *Runtime {
	t.Helper()
	r, err := NewRuntime(context.Background(), RuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	return r
}

func TestRuntime_NewCreates(t *testing.T) {
	r := newRuntime(t)
	if r == nil {
		t.Fatal("NewRuntime returned nil")
	}
}

func TestRuntime_LoadValidModule(t *testing.T) {
	r := newRuntime(t)
	p, err := r.Load(context.Background(), "adder", addWASM)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Name() != "adder" {
		t.Errorf("plugin name = %q, want adder", p.Name())
	}
	if _, ok := r.Get("adder"); !ok {
		t.Error("loaded plugin should be retrievable via Get")
	}
}

func TestRuntime_LoadInvalidModuleErrors(t *testing.T) {
	r := newRuntime(t)
	if _, err := r.Load(context.Background(), "bad", badWASM); err == nil {
		t.Error("loading invalid WASM should error")
	}
}

func TestRuntime_PluginExecutes(t *testing.T) {
	r := newRuntime(t)
	p, err := r.Load(context.Background(), "adder", addWASM)
	if err != nil {
		t.Fatal(err)
	}
	add := p.Module().ExportedFunction("add")
	if add == nil {
		t.Fatal("module should export 'add'")
	}
	res, err := add.Call(context.Background(), 2, 3)
	if err != nil {
		t.Fatalf("calling add: %v", err)
	}
	if len(res) != 1 || res[0] != 5 {
		t.Errorf("add(2,3) = %v, want [5]", res)
	}
}

func TestRuntime_MemoryLimitConfigured(t *testing.T) {
	// A 1 MB cap = 16 pages of 64 KiB. The module declares 1 page, which fits.
	r, err := NewRuntime(context.Background(), RuntimeConfig{MaxMemoryMB: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	if _, err := r.Load(context.Background(), "small", addWASM); err != nil {
		t.Errorf("a 1-page module should load under a 1 MB cap: %v", err)
	}
}

func TestRuntime_UnloadAndClose(t *testing.T) {
	r := newRuntime(t)
	if _, err := r.Load(context.Background(), "adder", addWASM); err != nil {
		t.Fatal(err)
	}
	if err := r.Unload("adder"); err != nil {
		t.Errorf("Unload: %v", err)
	}
	if _, ok := r.Get("adder"); ok {
		t.Error("plugin should be gone after Unload")
	}
	// Unloading a missing plugin is a no-op.
	if err := r.Unload("nonexistent"); err != nil {
		t.Errorf("Unload missing = %v, want nil", err)
	}
	if err := r.Close(context.Background()); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ensure the default CPU budget is plumbed onto loaded plugins.
func TestRuntime_PluginCPUBudget(t *testing.T) {
	r, _ := NewRuntime(context.Background(), RuntimeConfig{MaxCPUTime: 50 * time.Millisecond})
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	p, err := r.Load(context.Background(), "adder", addWASM)
	if err != nil {
		t.Fatal(err)
	}
	if p.MaxCPU() != 50*time.Millisecond {
		t.Errorf("plugin MaxCPU = %v, want 50ms", p.MaxCPU())
	}
}
