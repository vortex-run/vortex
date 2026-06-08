//go:build integration

package integration

import (
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
	"github.com/vortex-run/vortex/internal/tui"
)

// TestTUI_ClientConnects starts vortex and confirms the TUI client connects and
// reads health.
func TestTUI_ClientConnects(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	c := tui.NewClient(tui.ClientConfig{BaseURL: p.APIAddr})
	if !c.IsConnected() {
		t.Fatal("client should report connected to a live server")
	}
	h, err := c.Health()
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.ClusterName == "" {
		t.Error("health should report a cluster name")
	}
}

// TestTUI_ClientDisconnected confirms the client reports disconnected against a
// dead address.
func TestTUI_ClientDisconnected(t *testing.T) {
	c := tui.NewClient(tui.ClientConfig{BaseURL: "http://127.0.0.1:1"})
	if c.IsConnected() {
		t.Error("client should report disconnected against a dead server")
	}
	if _, err := c.Health(); err == nil {
		t.Error("Health against a dead server should error")
	}
}

// TestTUI_LogsEndpoint confirms /api/logs returns the boot logs.
func TestTUI_LogsEndpoint(t *testing.T) {
	secret := seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	req, _ := http.NewRequest(http.MethodGet, p.APIAddr+"/api/logs?limit=50", nil)
	req.Header.Set("X-API-Key", secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/logs: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/logs = %d, want 200", resp.StatusCode)
	}
}

// TestTUI_UICommandHelp confirms `vortex ui --help` works and lists the flags.
func TestTUI_UICommandHelp(t *testing.T) {
	bin := getNetBinary(t)
	out, err := exec.Command(bin, "ui", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("vortex ui --help: %v\n%s", err, out)
	}
	s := string(out)
	for _, flag := range []string{"--addr", "--key", "--start"} {
		if !strings.Contains(s, flag) {
			t.Errorf("ui --help missing %s:\n%s", flag, s)
		}
	}
}
