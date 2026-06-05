//go:build integration

package integration

import (
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// secretsConfig renders a vortex.cue declaring the given secret keys and no
// routes (secrets are independent of the data plane).
func secretsConfig(cluster string, keys string) string {
	return `cluster: { name: "` + cluster + `" }
tls: { acme_email: "a@b.com", provider: "internal", min_version: "TLS1.2" }
routes: []
security: {}
secrets: { keys: [` + keys + `] }
observability: { log_level: "info", log_sink: "stderr" }
`
}

func TestSecrets_SetAndList(t *testing.T) {
	storeDir := t.TempDir()
	t.Setenv("VORTEX_SECRET_STORE", storeDir)

	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, secretsConfig("secret-cluster", `"TEST_KEY"`))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	if out, code := p.Run(t, "secret", "--config", cfg, "set", "TEST_KEY", "hello123"); code != 0 {
		t.Fatalf("secret set (%d): %s", code, out)
	}
	out, code := p.Run(t, "secret", "--config", cfg, "list")
	if code != 0 {
		t.Fatalf("secret list (%d): %s", code, out)
	}
	if !strings.Contains(out, "TEST_KEY") || !strings.Contains(out, "[set]") {
		t.Errorf("list should show TEST_KEY [set]:\n%s", out)
	}
}

func TestSecrets_RevealValue(t *testing.T) {
	storeDir := t.TempDir()
	t.Setenv("VORTEX_SECRET_STORE", storeDir)

	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, secretsConfig("secret-cluster", `"TEST_KEY"`))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	if out, code := p.Run(t, "secret", "--config", cfg, "set", "TEST_KEY", "revealed-value"); code != 0 {
		t.Fatalf("secret set (%d): %s", code, out)
	}
	out, code := p.Run(t, "secret", "--config", cfg, "get", "TEST_KEY", "--reveal")
	if code != 0 {
		t.Fatalf("secret get --reveal (%d): %s", code, out)
	}
	if !strings.Contains(out, "revealed-value") {
		t.Errorf("get --reveal should print the value:\n%s", out)
	}
}

func TestSecrets_MissingSecretWarnsNotFails(t *testing.T) {
	storeDir := t.TempDir()
	t.Setenv("VORTEX_SECRET_STORE", storeDir)

	bin := getNetBinary(t)
	// Declare a secret that is never set.
	cfg := testutil.WriteTestConfig(t, secretsConfig("secret-cluster", `"MISSING_KEY"`))
	p := testutil.StartVortex(t, bin, cfg) // must start successfully despite the missing secret
	defer p.Stop(t)

	// /health is 200 (StartVortex already waited for it).
	if h := p.Health(t); h["status"] != "ok" {
		t.Errorf("health status = %v, want ok", h["status"])
	}

	// The startup log warns about the unset declared secret.
	if logs := p.Logs(); !strings.Contains(logs, "declared secret not set") || !strings.Contains(logs, "MISSING_KEY") {
		t.Errorf("startup log should warn about MISSING_KEY:\n%s", logs)
	}
}
