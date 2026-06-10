//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// TestHealing_Enabled confirms self-healing comes up and reports status.
func TestHealing_Enabled(t *testing.T) {
	secret := seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	// The startup log should announce self-healing.
	if !strings.Contains(p.Logs(), "self-healing enabled") {
		t.Errorf("startup log missing 'self-healing enabled':\n%s", p.Logs())
	}

	req, _ := http.NewRequest(http.MethodGet, p.APIAddr+"/api/healing/status", nil)
	req.Header.Set("X-API-Key", secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/healing/status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/healing/status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Healthy bool `json:"healthy"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Healthy {
		t.Error("freshly started server should report healthy")
	}
}

// TestHealing_StatusRequiresAuth confirms the endpoint is auth-gated.
func TestHealing_StatusRequiresAuth(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Get(p.APIAddr + "/api/healing/status")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status without key = %d, want 401", resp.StatusCode)
	}
}
