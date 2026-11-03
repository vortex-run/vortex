//go:build integration

package testutil

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestVortexProcessEnvIsolatesWindowsCacheDir ensures StartVortex children do
// not inherit the developer's LOCALAPPDATA (UserCacheDir), which previously
// caused shared %LocalAppData%\vortex state across integration runs.
func TestVortexProcessEnvIsolatesWindowsCacheDir(t *testing.T) {
	home := t.TempDir()
	p := &VortexProcess{ConfigHome: home}
	env := p.env()
	want := map[string]string{
		"XDG_CONFIG_HOME": home,
		"XDG_CACHE_HOME":  home,
		"AppData":         home,
		"APPDATA":         home,
		"LocalAppData":    home,
		"LOCALAPPDATA":    home,
		// Children must listen on loopback only (no network exposure, no
		// Windows Firewall prompts).
		"VORTEX_API_LISTEN":     "127.0.0.1:9090",
		"VORTEX_EDGE_BIND_HOST": "127.0.0.1",
	}
	got := map[string]string{}
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if _, need := want[k]; need {
			got[k] = v
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// Last-wins for duplicate keys from os.Environ: our append must be after
	// the parent env, so values match ConfigHome even if the parent set them.
	if !filepath.IsAbs(home) {
		t.Fatalf("ConfigHome should be absolute temp path, got %q", home)
	}
}

func TestWaitPortFree_UnoccupiedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}

	if err := waitPortFree(t, addr, 2*time.Second); err != nil {
		t.Fatalf("waitPortFree on free port: %v", err)
	}
}

func TestWaitPortFree_OccupiedPortReturnsError(t *testing.T) {
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	addr := holder.Addr().String()

	err = waitPortFree(t, addr, 300*time.Millisecond)
	if err == nil {
		t.Fatal("waitPortFree: want error while port is held, got nil")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("error %q should mention occupied address %q", err, addr)
	}
}
