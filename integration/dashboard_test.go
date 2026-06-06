//go:build integration

package integration

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// isolatedAuditLog points VORTEX_AUDIT_LOG at a fresh per-test file so the
// chain is written and verified with one cluster key (the shared default cache
// path can accumulate entries from prior runs under different keys).
func isolatedAuditLog(t *testing.T) {
	t.Helper()
	t.Setenv("VORTEX_AUDIT_LOG", filepath.Join(t.TempDir(), "audit.log"))
}

func TestDashboard_Serves(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Get(p.APIAddr + "/dashboard/")
	if err != nil {
		t.Fatalf("GET /dashboard/: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/dashboard/ status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "VORTEX") {
		t.Errorf("dashboard HTML should contain VORTEX title:\n%s", body)
	}
}

func TestDashboard_APIStatus(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Get(p.APIAddr + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/status status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"node_id"`) {
		t.Errorf("/api/status missing node_id field:\n%s", body)
	}
}

func TestDashboard_AuditVerify(t *testing.T) {
	isolatedAuditLog(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Post(p.APIAddr+"/api/audit/verify", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/audit/verify: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/audit/verify status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"valid":true`) {
		t.Errorf("audit verify should be valid on a fresh log:\n%s", body)
	}
}
