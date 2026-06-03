package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/config"
)

const sampleConfig = `
cluster: {name: "live-test"}
tls: {acme_email: "a@b.com"}
routes: []
security: {}
secrets: {}
observability: {}
`

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestHealthReflectsConfigReload is the live equivalent of the M1.2 manual
// check: start the server, hit /health, reload config, and confirm /health
// reports the new config hash — proving the atomic swap is visible to live
// readers without a restart.
func TestHealthReflectsConfigReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vortex.cue")
	if err := os.WriteFile(path, []byte(sampleConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	mgr, err := config.NewManager(path, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Bind an ephemeral port to avoid clashes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv := New(addr, mgr.Holder(), "test", discardLogger())
	srv.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	waitReady(t, addr)

	first := getHealth(t, addr)
	if first.Status != "ok" || first.ConfigHash == "" {
		t.Fatalf("unexpected health: %+v", first)
	}

	// Reload with a changed config.
	newCfg := `cluster: {name: "live-test-2"}
tls: {acme_email: "a@b.com"}
routes: []
security: {}
secrets: {}
observability: {}
`
	if err := os.WriteFile(path, []byte(newCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reload(); err != nil {
		t.Fatalf("reload failed: %v", err)
	}

	second := getHealth(t, addr)
	if second.ConfigHash == first.ConfigHash {
		t.Error("config hash should change after reload")
	}
	if second.ClusterName != "live-test-2" {
		t.Errorf("cluster_name = %q, want live-test-2", second.ClusterName)
	}
}

func waitReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not become ready")
}

func getHealth(t *testing.T, addr string) healthResponse {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var hr healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return hr
}
