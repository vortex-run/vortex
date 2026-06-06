//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/testutil"
)

func TestAPI_HealthEndpoint(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.MinimalConfig)
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Get(p.APIAddr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	h := p.Health(t)
	for _, field := range []string{"status", "version", "config_hash", "cluster_name", "uptime"} {
		if _, ok := h[field]; !ok {
			t.Errorf("health response missing field %q", field)
		}
	}
}

func TestAPI_ReadyEndpoint(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.MinimalConfig)
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Get(p.APIAddr + "/ready")
	if err != nil {
		t.Fatalf("GET /ready: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/ready status = %d, want 200", resp.StatusCode)
	}
}

func TestAPI_ReloadEndpoint_LocalhostOnly(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.MinimalConfig)
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Post(p.APIAddr+"/internal/reload", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /internal/reload: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/internal/reload from localhost status = %d, want 200", resp.StatusCode)
	}
}

func TestAPI_ShutdownEndpoint_LocalhostOnly(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.MinimalConfig)
	p := testutil.StartVortex(t, bin, cfg)

	resp, err := http.Post(p.APIAddr+"/internal/shutdown", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /internal/shutdown: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/internal/shutdown status = %d, want 200", resp.StatusCode)
	}

	// The process should exit cleanly within 5s.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, e := http.Get(p.APIAddr + "/health")
		if e != nil {
			// Connection refused → server is gone.
			p.MarkStopped()
			return
		}
		_ = r.Body.Close()
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("server did not shut down within 5s after /internal/shutdown")
}

func TestAPI_UnknownRoute(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.MinimalConfig)
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Get(p.APIAddr + "/this-does-not-exist")
	if err != nil {
		t.Fatalf("GET unknown route: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown route status = %d, want 404", resp.StatusCode)
	}
}

func TestAPI_HealthReturnsNewHashAfterReload(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.MinimalConfig)
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	hash1 := p.Health(t)["config_hash"]

	// Change the cluster name so the config hash differs, then reload via the
	// internal endpoint.
	v2 := strings.Replace(testutil.MinimalConfig, `name: "test-cluster"`, `name: "test-cluster-2"`, 1)
	if err := writeFile(cfg, v2); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(p.APIAddr+"/internal/reload", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /internal/reload: %v", err)
	}
	_ = resp.Body.Close()
	time.Sleep(300 * time.Millisecond)

	hash2 := p.Health(t)["config_hash"]
	if hash1 == hash2 {
		t.Errorf("config_hash should change after reload (still %v)", hash1)
	}
}
