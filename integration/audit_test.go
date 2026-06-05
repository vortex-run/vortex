//go:build integration

package integration

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/testutil"
)

// auditConfig renders a config with a fixed cluster name so the audit HMAC key
// is stable between the server and the `vortex audit` CLI.
func auditConfig() string {
	return `cluster: { name: "audit-int" }
tls: { acme_email: "a@b.com", provider: "internal", min_version: "TLS1.2" }
routes: []
security: {}
secrets: { store: "local", keys: ["AUDIT_KEY"] }
observability: { log_level: "info", log_sink: "stderr" }
`
}

func TestAudit_SecretSetRecorded(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	t.Setenv("VORTEX_AUDIT_LOG", auditPath)
	t.Setenv("VORTEX_SECRET_STORE", t.TempDir())

	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, auditConfig())
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	// Set a secret via the CLI — this must append a secret.set audit entry.
	if out, code := p.Run(t, "secret", "--config", cfg, "set", "AUDIT_KEY", "hidden-value"); code != 0 {
		t.Fatalf("secret set (%d): %s", code, out)
	}

	out, code := p.Run(t, "audit", "--config", cfg, "export", "--format", "json")
	if code != 0 {
		t.Fatalf("audit export (%d): %s", code, out)
	}
	if !strings.Contains(out, "secret.set") {
		t.Errorf("audit export missing secret.set entry:\n%s", out)
	}
	// The secret value must NEVER appear in the audit log.
	if strings.Contains(out, "hidden-value") {
		t.Error("audit log leaked the secret value")
	}
}

func TestAudit_ReloadRecorded(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	t.Setenv("VORTEX_AUDIT_LOG", auditPath)
	t.Setenv("VORTEX_SECRET_STORE", t.TempDir())

	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, auditConfig())
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	// Trigger a reload through the loopback control-plane endpoint.
	resp, err := http.Post(p.APIAddr+"/internal/reload", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /internal/reload: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/internal/reload status = %d, want 200", resp.StatusCode)
	}
	time.Sleep(200 * time.Millisecond)

	out, code := p.Run(t, "audit", "--config", cfg, "export", "--format", "json", "--action", "config.reload")
	if code != 0 {
		t.Fatalf("audit export (%d): %s", code, out)
	}
	if !strings.Contains(out, "config.reload") {
		t.Errorf("audit export missing config.reload entry:\n%s", out)
	}
}

func TestAudit_StartEventAndVerify(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	t.Setenv("VORTEX_AUDIT_LOG", auditPath)
	t.Setenv("VORTEX_SECRET_STORE", t.TempDir())

	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, auditConfig())
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	// The startup event is recorded; verify the chain is intact.
	out, code := p.Run(t, "audit", "--config", cfg, "verify")
	if code != 0 {
		t.Fatalf("audit verify (%d): %s", code, out)
	}
	if !strings.Contains(out, "integrity verified") {
		t.Errorf("audit verify output = %q", out)
	}

	exp, _ := p.Run(t, "audit", "--config", cfg, "export", "--format", "json")
	if !strings.Contains(exp, "vortex.start") {
		t.Errorf("audit export missing vortex.start entry:\n%s", exp)
	}
}
