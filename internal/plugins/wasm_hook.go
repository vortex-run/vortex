package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// WASMHook is a Hook backed by a WebAssembly module loaded into the sandboxed
// runtime. The module must export:
//
//	memory                       — linear memory
//	alloc(size i32) i32          — allocate `size` bytes, return the pointer
//	handle(ptr i32, len i32) i64 — process the JSON HookInput at [ptr,len) and
//	                               return a packed result: (outPtr<<32)|outLen,
//	                               pointing at a JSON HookOutput in memory.
//
// Input is the JSON-encoded HookInput; output is the JSON-encoded HookOutput.
type WASMHook struct {
	name     string
	hookType HookType
	plugin   *Plugin
}

// NewWASMHook loads wasm into runtime under name and returns a Hook of the given
// type. The module is validated to export the required functions.
func NewWASMHook(runtime *Runtime, name string, wasm []byte, hookType HookType) (*WASMHook, error) {
	p, err := runtime.Load(context.Background(), name, wasm)
	if err != nil {
		return nil, err
	}
	mod := p.Module()
	if mod.ExportedFunction("handle") == nil {
		return nil, fmt.Errorf("plugins: WASM hook %s must export 'handle'", name)
	}
	if mod.ExportedFunction("alloc") == nil {
		return nil, fmt.Errorf("plugins: WASM hook %s must export 'alloc'", name)
	}
	if mod.Memory() == nil {
		return nil, fmt.Errorf("plugins: WASM hook %s must export 'memory'", name)
	}
	return &WASMHook{name: name, hookType: hookType, plugin: p}, nil
}

// Name returns the hook's name.
func (h *WASMHook) Name() string { return h.name }

// Type returns the hook's lifecycle type.
func (h *WASMHook) Type() HookType { return h.hookType }

// Execute serialises in to JSON, copies it into WASM memory, calls handle(),
// reads the JSON HookOutput back, and deserialises it. The call is bounded by
// the plugin's CPU-time budget; exceeding it cancels execution and returns an
// error.
func (h *WASMHook) Execute(ctx context.Context, in HookInput) (HookOutput, error) {
	callCtx, cancel := context.WithTimeout(ctx, h.plugin.MaxCPU())
	defer cancel()

	mod := h.plugin.Module()
	alloc := mod.ExportedFunction("alloc")
	handle := mod.ExportedFunction("handle")
	mem := mod.Memory()

	payload, err := json.Marshal(in)
	if err != nil {
		return HookOutput{}, fmt.Errorf("plugins: encoding hook input: %w", err)
	}

	// Allocate input buffer in WASM memory and write the payload.
	allocRes, err := alloc.Call(callCtx, uint64(len(payload)))
	if err != nil {
		return HookOutput{}, fmt.Errorf("plugins: alloc failed: %w", err)
	}
	inPtr := uint32(allocRes[0])
	if !mem.Write(inPtr, payload) {
		return HookOutput{}, errors.New("plugins: writing hook input to WASM memory failed")
	}

	// Invoke the module's handler.
	res, err := handle.Call(callCtx, uint64(inPtr), uint64(len(payload)))
	if err != nil {
		return HookOutput{}, fmt.Errorf("plugins: hook %s handle failed: %w", h.name, err)
	}
	if len(res) != 1 {
		return HookOutput{}, errors.New("plugins: handle must return a single i64")
	}

	// Unpack the result pointer/length and read the output JSON.
	packed := res[0]
	outPtr := uint32(packed >> 32)
	outLen := uint32(packed & 0xffffffff)
	outBytes, ok := mem.Read(outPtr, outLen)
	if !ok {
		return HookOutput{}, errors.New("plugins: reading hook output from WASM memory failed")
	}

	var out HookOutput
	if err := json.Unmarshal(outBytes, &out); err != nil {
		return HookOutput{}, fmt.Errorf("plugins: decoding hook output: %w", err)
	}
	return out, nil
}
