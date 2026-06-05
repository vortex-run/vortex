// Package plugins implements VORTEX's WebAssembly plugin system (build plan M6):
// a sandboxed wazero runtime, a request/response hook chain, WASM-backed hooks,
// and a plugin registry. Plugins run with no filesystem or network access and a
// bounded memory and CPU-time budget, so untrusted modules cannot escape the
// sandbox. wazero is pure Go (no CGO).
package plugins

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// wasmPageBytes is the size of one WebAssembly memory page (64 KiB).
const wasmPageBytes = 64 * 1024

// RuntimeConfig bounds plugin resource use.
type RuntimeConfig struct {
	MaxMemoryMB int           // per-plugin memory cap; default 64 MB
	MaxCPUTime  time.Duration // per-call CPU-time budget; default 100ms
}

// Runtime is a sandboxed wazero runtime hosting compiled plugins. It is safe for
// concurrent use.
type Runtime struct {
	cfg RuntimeConfig
	rt  wazero.Runtime

	mu      sync.Mutex
	plugins map[string]*Plugin
}

// Plugin is a loaded, instantiated WASM module ready to invoke.
type Plugin struct {
	name   string
	mod    api.Module
	maxCPU time.Duration
}

// NewRuntime creates a sandboxed runtime. Memory is capped per cfg
// (default 64 MB) and module execution is interruptible so a per-call context
// deadline can terminate runaway plugins.
func NewRuntime(ctx context.Context, cfg RuntimeConfig) (*Runtime, error) {
	if cfg.MaxMemoryMB <= 0 {
		cfg.MaxMemoryMB = 64
	}
	if cfg.MaxCPUTime <= 0 {
		cfg.MaxCPUTime = 100 * time.Millisecond
	}

	pages := uint32(cfg.MaxMemoryMB * 1024 * 1024 / wasmPageBytes)
	rc := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(pages).
		WithCloseOnContextDone(true) // honour ctx deadlines mid-execution

	rt := wazero.NewRuntimeWithConfig(ctx, rc)
	return &Runtime{
		cfg:     cfg,
		rt:      rt,
		plugins: make(map[string]*Plugin),
	}, nil
}

// Load compiles and instantiates a WASM module under name. The module runs with
// no host imports (no filesystem, no network), so it is fully sandboxed. An
// already-loaded name is replaced.
func (r *Runtime) Load(ctx context.Context, name string, wasm []byte) (*Plugin, error) {
	if name == "" {
		return nil, errors.New("plugins: plugin name must not be empty")
	}
	compiled, err := r.rt.CompileModule(ctx, wasm)
	if err != nil {
		return nil, fmt.Errorf("plugins: compiling %s: %w", name, err)
	}

	// Instantiate with an anonymous module config and no imported host functions:
	// the module gets memory but no access to the host environment.
	mod, err := r.rt.InstantiateModule(ctx, compiled,
		wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return nil, fmt.Errorf("plugins: instantiating %s: %w", name, err)
	}

	p := &Plugin{name: name, mod: mod, maxCPU: r.cfg.MaxCPUTime}
	r.mu.Lock()
	if old, ok := r.plugins[name]; ok {
		_ = old.mod.Close(ctx)
	}
	r.plugins[name] = p
	r.mu.Unlock()
	return p, nil
}

// Unload removes and closes the plugin named name. It is idempotent.
func (r *Runtime) Unload(name string) error {
	r.mu.Lock()
	p, ok := r.plugins[name]
	delete(r.plugins, name)
	r.mu.Unlock()
	if !ok {
		return nil
	}
	return p.mod.Close(context.Background())
}

// Get returns the loaded plugin named name, if present.
func (r *Runtime) Get(name string) (*Plugin, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.plugins[name]
	return p, ok
}

// Close tears down the runtime and every plugin it created.
func (r *Runtime) Close(ctx context.Context) error {
	if err := r.rt.Close(ctx); err != nil {
		return fmt.Errorf("plugins: closing runtime: %w", err)
	}
	return nil
}

// Name returns the plugin's name.
func (p *Plugin) Name() string { return p.name }

// Module exposes the underlying wazero module (used by WASM-backed hooks).
func (p *Plugin) Module() api.Module { return p.mod }

// MaxCPU returns the per-call CPU-time budget for this plugin.
func (p *Plugin) MaxCPU() time.Duration { return p.maxCPU }
