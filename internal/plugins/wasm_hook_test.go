package plugins

import (
	"context"
	"testing"
	"time"
)

// leb128 encodes n as unsigned LEB128.
func leb128(n uint32) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

// section wraps a section body with its id and LEB128 length.
func section(id byte, body []byte) []byte {
	out := []byte{id}
	out = append(out, leb128(uint32(len(body)))...)
	return append(out, body...)
}

// vec prefixes items with a LEB128 count.
func vec(count uint32, items []byte) []byte {
	return append(leb128(count), items...)
}

// buildHookWASM emits a WASM module that ignores its input and always returns
// the JSON `output` from a data segment. It exports:
//
//	memory                       (1 page)
//	alloc(size i32) i32          — returns a fixed scratch pointer (1024)
//	handle(ptr i32, len i32) i64 — returns (outPtr<<32 | outLen)
//
// The output bytes are placed at offset 0 via an active data segment; alloc
// hands out a region well past them.
func buildHookWASM(output []byte) []byte {
	const allocPtr = 1024
	outLen := uint32(len(output))

	// Type section: two types.
	//   type 0: (i32) -> i32        (alloc)
	//   type 1: (i32,i32) -> i64    (handle)
	types := vec(2,
		concat(
			[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f},       // (i32)->i32
			[]byte{0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7e}, // (i32,i32)->i64
		),
	)

	// Function section: func 0 uses type 0, func 1 uses type 1.
	funcs := vec(2, []byte{0x00, 0x01})

	// Memory section: 1 memory, min 1 page.
	mem := vec(1, []byte{0x00, 0x01})

	// Export section: memory, alloc(func0), handle(func1).
	exports := vec(3, concat(
		exportEntry("memory", 0x02, 0),
		exportEntry("alloc", 0x00, 0),
		exportEntry("handle", 0x00, 1),
	))

	// Code section.
	//   alloc body: i32.const allocPtr; end
	allocBody := concat(
		[]byte{0x00},                   // 0 locals
		[]byte{0x41}, leb128(allocPtr), // i32.const allocPtr
		[]byte{0x0b}, // end
	)
	//   handle body: i64.const (outPtr<<32 | outLen); end   (outPtr = 0)
	packed := uint64(outLen) // outPtr 0 -> high bits zero
	handleBody := concat(
		[]byte{0x00},                        // 0 locals
		[]byte{0x42}, sleb64(int64(packed)), // i64.const packed
		[]byte{0x0b}, // end
	)
	code := vec(2, concat(
		append(leb128(uint32(len(allocBody))), allocBody...),
		append(leb128(uint32(len(handleBody))), handleBody...),
	))

	// Data section: one active segment writing `output` at offset 0.
	dataSeg := concat(
		[]byte{0x00},             // segment 0, active, memory 0
		[]byte{0x41, 0x00, 0x0b}, // i32.const 0; end  (offset expr)
		leb128(outLen), output,   // bytes
	)
	data := vec(1, dataSeg)

	out := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	out = append(out, section(0x01, types)...)
	out = append(out, section(0x03, funcs)...)
	out = append(out, section(0x05, mem)...)
	out = append(out, section(0x07, exports)...)
	out = append(out, section(0x0a, code)...)
	out = append(out, section(0x0b, data)...)
	return out
}

// exportEntry encodes one export (name, kind, index).
func exportEntry(name string, kind byte, idx uint32) []byte {
	out := append(leb128(uint32(len(name))), []byte(name)...)
	out = append(out, kind)
	return append(out, leb128(idx)...)
}

// sleb64 encodes n as signed LEB128 (sufficient for our small positive values).
func sleb64(n int64) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		signBit := b & 0x40
		if (n == 0 && signBit == 0) || (n == -1 && signBit != 0) {
			out = append(out, b)
			return out
		}
		out = append(out, b|0x80)
	}
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func newWASMRuntime(t *testing.T) *Runtime {
	t.Helper()
	r, err := NewRuntime(context.Background(), RuntimeConfig{MaxCPUTime: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	return r
}

func TestWASMHook_AllowAll(t *testing.T) {
	r := newWASMRuntime(t)
	wasm := buildHookWASM([]byte(`{"allow":true}`))
	h, err := NewWASMHook(r, "allow", wasm, HookPreRequest)
	if err != nil {
		t.Fatalf("NewWASMHook: %v", err)
	}
	out, err := h.Execute(context.Background(), HookInput{Method: "GET", Path: "/x"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.Allow {
		t.Error("allow-all plugin should allow")
	}
}

func TestWASMHook_DenyAll(t *testing.T) {
	r := newWASMRuntime(t)
	wasm := buildHookWASM([]byte(`{"allow":false,"status":403}`))
	h, err := NewWASMHook(r, "deny", wasm, HookPreRequest)
	if err != nil {
		t.Fatalf("NewWASMHook: %v", err)
	}
	out, err := h.Execute(context.Background(), HookInput{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Allow {
		t.Error("deny-all plugin should deny")
	}
	if out.Status != 403 {
		t.Errorf("Status = %d, want 403", out.Status)
	}
}

func TestWASMHook_OutputDeserialized(t *testing.T) {
	r := newWASMRuntime(t)
	wasm := buildHookWASM([]byte(`{"allow":true,"modified":true,"headers":{"X-Plugin":["v"]}}`))
	h, err := NewWASMHook(r, "mod", wasm, HookPostResponse)
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.Execute(context.Background(), HookInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Modified || out.Headers["X-Plugin"][0] != "v" {
		t.Errorf("output not deserialized correctly: %+v", out)
	}
}

func TestWASMHook_MissingExportsRejected(t *testing.T) {
	r := newWASMRuntime(t)
	// addWASM (from runtime_test.go) exports add+memory but not alloc/handle.
	if _, err := NewWASMHook(r, "bad", addWASM, HookPreRequest); err == nil {
		t.Error("a module without alloc/handle should be rejected")
	}
}

func TestWASMHook_InputWrittenToMemory(t *testing.T) {
	// The handler ignores input but the host must still write it without error;
	// a large input exercises the alloc+Write path.
	r := newWASMRuntime(t)
	wasm := buildHookWASM([]byte(`{"allow":true}`))
	h, err := NewWASMHook(r, "io", wasm, HookPreRequest)
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 4096)
	out, err := h.Execute(context.Background(), HookInput{Body: big})
	if err != nil {
		t.Fatalf("Execute with large input: %v", err)
	}
	if !out.Allow {
		t.Error("should allow")
	}
}

// sanity: the generated module's packed return must round-trip through the
// pointer/length unpacking the host does.
func TestWASMHook_PackedReturnRoundTrip(t *testing.T) {
	out := []byte(`{"allow":true}`)
	wasm := buildHookWASM(out)
	r := newWASMRuntime(t)
	p, err := r.Load(context.Background(), "rt", wasm)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Module().ExportedFunction("handle").Call(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	packed := res[0]
	gotLen := uint32(packed & 0xffffffff)
	if gotLen != uint32(len(out)) {
		t.Errorf("packed len = %d, want %d", gotLen, len(out))
	}
	// Confirm the bytes at the returned pointer match.
	ptr := uint32(packed >> 32)
	b, ok := p.Module().Memory().Read(ptr, gotLen)
	if !ok || string(b) != string(out) {
		t.Errorf("memory at returned ptr = %q, want %q", b, out)
	}
}
