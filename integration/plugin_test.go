//go:build integration

package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/plugins"
	"github.com/vortex-run/vortex/internal/testutil"
)

// hookWASM builds a minimal WASM module that ignores its input and always
// returns the JSON `output` (a HookOutput). It exports memory, alloc, and handle
// per the VORTEX plugin ABI. Hand-encoded so the test needs no WASM toolchain.
func hookWASM(output []byte) []byte {
	leb := func(n uint32) []byte {
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
	sec := func(id byte, body []byte) []byte {
		return append(append([]byte{id}, leb(uint32(len(body)))...), body...)
	}
	vec := func(count uint32, items []byte) []byte { return append(leb(count), items...) }
	cat := func(parts ...[]byte) []byte {
		var out []byte
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	exp := func(name string, kind byte, idx uint32) []byte {
		return cat(leb(uint32(len(name))), []byte(name), []byte{kind}, leb(idx))
	}
	outLen := uint32(len(output))

	types := vec(2, cat(
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f},       // (i32)->i32
		[]byte{0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7e}, // (i32,i32)->i64
	))
	funcs := vec(2, []byte{0x00, 0x01})
	mem := vec(1, []byte{0x00, 0x01})
	exports := vec(3, cat(exp("memory", 0x02, 0), exp("alloc", 0x00, 0), exp("handle", 0x00, 1)))

	allocBody := cat([]byte{0x00, 0x41}, leb(1024), []byte{0x0b})
	// i64.const outLen (outPtr 0); end. outLen < 64 so single-byte LEB.
	handleBody := cat([]byte{0x00, 0x42}, []byte{byte(outLen)}, []byte{0x0b})
	code := vec(2, cat(
		append(leb(uint32(len(allocBody))), allocBody...),
		append(leb(uint32(len(handleBody))), handleBody...),
	))
	dataSeg := cat([]byte{0x00, 0x41, 0x00, 0x0b}, leb(outLen), output)
	data := vec(1, dataSeg)

	return cat(
		[]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00},
		sec(0x01, types), sec(0x03, funcs), sec(0x05, mem),
		sec(0x07, exports), sec(0x0a, code), sec(0x0b, data),
	)
}

// installPlugin writes a plugin into a registry rooted at dir (the same
// VORTEX_PLUGIN_DIR the server reads).
func installPlugin(t *testing.T, dir, name string, output []byte) {
	t.Helper()
	reg, err := plugins.NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	wasm := hookWASM(output)
	m := plugins.PluginManifest{
		Name: name, Version: "1.0.0", Checksum: reg.Checksum(wasm),
		HookTypes: []plugins.HookType{plugins.HookPreRequest},
	}
	if err := reg.Install(m, wasm); err != nil {
		t.Fatal(err)
	}
}

// pluginRoute renders an http route that declares the given plugin.
func pluginRoute(name string, listen int, beHost string, bePort int, plugin string) string {
	return `{name: "` + name + `", protocol: "http", listen: ` + strconv.Itoa(listen) +
		`, backends: [{host: "` + beHost + `", port: ` + strconv.Itoa(bePort) +
		`}], plugins: ["` + plugin + `"]}`
}

func TestPlugin_NoPlugins(t *testing.T) {
	bin := getNetBinary(t)
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "backend")
	}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	listen := testutil.FreePort(t)
	cfg := testutil.WriteTestConfig(t, netConfig(httpRoute("web", listen, beHost, bePort)))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	addr := "127.0.0.1:" + strconv.Itoa(listen)
	waitListening(t, addr)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "backend" {
		t.Errorf("no plugins: status=%d body=%q, want 200 'backend'", resp.StatusCode, body)
	}
}

func TestPlugin_HookChainPassthrough(t *testing.T) {
	pluginDir := t.TempDir()
	t.Setenv("VORTEX_PLUGIN_DIR", pluginDir)
	installPlugin(t, pluginDir, "allow", []byte(`{"allow":true}`))

	bin := getNetBinary(t)
	var reached bool
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = io.WriteString(w, "backend")
	}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	listen := testutil.FreePort(t)
	cfg := testutil.WriteTestConfig(t, netConfig(pluginRoute("web", listen, beHost, bePort, "allow")))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	addr := "127.0.0.1:" + strconv.Itoa(listen)
	waitListening(t, addr)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("allow plugin: status = %d, want 200", resp.StatusCode)
	}
	if !reached {
		t.Error("allow plugin should let the request reach the backend")
	}
}

func TestPlugin_HookChainDeny(t *testing.T) {
	pluginDir := t.TempDir()
	t.Setenv("VORTEX_PLUGIN_DIR", pluginDir)
	installPlugin(t, pluginDir, "deny", []byte(`{"allow":false}`))

	bin := getNetBinary(t)
	var reached bool
	be := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	listen := testutil.FreePort(t)
	cfg := testutil.WriteTestConfig(t, netConfig(pluginRoute("web", listen, beHost, bePort, "deny")))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	addr := "127.0.0.1:" + strconv.Itoa(listen)
	waitListening(t, addr)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("deny plugin: status = %d, want 403", resp.StatusCode)
	}
	if reached {
		t.Error("deny plugin should block the request before the backend")
	}
	if !strings.Contains(string(body), "denied by plugin") {
		t.Errorf("403 body = %q, want 'denied by plugin'", body)
	}
}
