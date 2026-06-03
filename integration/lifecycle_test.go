//go:build integration

package integration

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/testutil"
)

// writeFile overwrites the file at path with content (used to mutate a config
// in place between reloads).
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestLifecycle_GracefulShutdown(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.MinimalConfig)
	p := testutil.StartVortex(t, bin, cfg)

	if h := p.Health(t); h["status"] != "ok" {
		t.Fatalf("expected healthy server, got %v", h["status"])
	}

	// Stop asserts a clean exit (code 0) internally.
	p.Stop(t)

	// The server should no longer be listening.
	if resp, err := http.Get(p.APIAddr + "/health"); err == nil {
		_ = resp.Body.Close()
		t.Error("server should be gone after shutdown")
	}
}

func TestLifecycle_ConfigReload(t *testing.T) {
	bin := testutil.BuildBinary(t)
	v1 := strings.Replace(testutil.MinimalConfig, `name: "test-cluster"`, `name: "reload-test-v1"`, 1)
	cfg := testutil.WriteTestConfig(t, v1)
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	h1 := p.Health(t)
	if h1["cluster_name"] != "reload-test-v1" {
		t.Fatalf("cluster_name = %v, want reload-test-v1", h1["cluster_name"])
	}
	hash1 := h1["config_hash"]

	// Rewrite the same path with v2 and reload.
	v2 := strings.Replace(testutil.MinimalConfig, `name: "test-cluster"`, `name: "reload-test-v2"`, 1)
	if err := writeFile(cfg, v2); err != nil {
		t.Fatal(err)
	}
	out, code := p.Run(t, "reload", "--config", cfg)
	if code != 0 {
		t.Fatalf("reload exit code = %d\n%s", code, out)
	}
	time.Sleep(500 * time.Millisecond)

	h2 := p.Health(t)
	if h2["cluster_name"] != "reload-test-v2" {
		t.Errorf("after reload cluster_name = %v, want reload-test-v2", h2["cluster_name"])
	}
	if h1["config_hash"] == h2["config_hash"] {
		t.Errorf("config_hash should change after reload (was %v)", hash1)
	}
}

func TestLifecycle_InvalidReloadKeepsRunning(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.MinimalConfig)
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	h1 := p.Health(t)
	if h1["status"] != "ok" {
		t.Fatalf("expected healthy server")
	}
	original := h1["cluster_name"]

	// Corrupt the config and reload — the server must keep running on the old.
	if err := writeFile(cfg, testutil.InvalidConfig); err != nil {
		t.Fatal(err)
	}
	_, _ = p.Run(t, "reload", "--config", cfg)
	time.Sleep(500 * time.Millisecond)

	h2 := p.Health(t)
	if h2["status"] != "ok" {
		t.Error("server should still be healthy after an invalid reload")
	}
	if h2["cluster_name"] != original {
		t.Errorf("cluster_name should be unchanged (%v), got %v", original, h2["cluster_name"])
	}
}

func TestLifecycle_StatusCommand(t *testing.T) {
	bin := testutil.BuildBinary(t)
	cfg := testutil.WriteTestConfig(t, testutil.MinimalConfig)
	p := testutil.StartVortex(t, bin, cfg)

	out, code := p.Run(t, "status")
	if code != 0 {
		t.Fatalf("status exit code = %d\n%s", code, out)
	}
	for _, want := range []string{"Status:   running", "PID:", "Uptime:", "Cluster:"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}

	p.Stop(t)

	out, code = p.Run(t, "status")
	if code != 1 {
		t.Errorf("status after stop exit code = %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "not running") {
		t.Errorf("status after stop should say 'not running':\n%s", out)
	}
}

func TestLifecycle_StopWhenNotRunning(t *testing.T) {
	bin := testutil.BuildBinary(t)
	out, code := testutil.RunBinary(t, bin, "stop")
	if code != 0 {
		t.Errorf("stop when not running should exit 0, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "not running") {
		t.Errorf("output should contain 'not running':\n%s", out)
	}
}
