//go:build integration

package integration

import (
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// singleNodeConfig is a config with no extra cluster peers (single-node mode).
func singleNodeConfig() string {
	return `cluster: { name: "cluster-int" }
tls: { acme_email: "a@b.com", provider: "internal", min_version: "TLS1.2" }
routes: []
security: {}
secrets: {}
observability: { log_level: "info", log_sink: "stderr" }
`
}

func TestCluster_SingleNodeStarts(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, singleNodeConfig())
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	if logs := p.Logs(); !strings.Contains(logs, "running in single-node mode") {
		t.Errorf("startup log should report single-node mode:\n%s", logs)
	}
	if h := p.Health(t); h["status"] != "ok" {
		t.Errorf("health status = %v, want ok", h["status"])
	}
}

func TestCluster_StatusCommand(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, singleNodeConfig())
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	out, code := p.Run(t, "cluster", "--config", cfg, "status")
	if code != 0 {
		t.Fatalf("cluster status (%d): %s", code, out)
	}
	if !strings.Contains(out, "Node ID:") {
		t.Errorf("status output missing Node ID:\n%s", out)
	}
	if !strings.Contains(out, "single-node") {
		t.Errorf("status output should report single-node:\n%s", out)
	}
}
