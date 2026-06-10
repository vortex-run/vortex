//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// TestDevOps_ServersEndpointEmpty confirms the DevOps servers endpoint is wired
// and returns an empty array when no servers are configured (no real SSH).
func TestDevOps_ServersEndpointEmpty(t *testing.T) {
	secret := seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	req, _ := http.NewRequest(http.MethodGet, p.APIAddr+"/api/devops/servers", nil)
	req.Header.Set("X-API-Key", secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/devops/servers: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/devops/servers = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Servers []any `json:"servers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Servers == nil {
		t.Error("servers should be a (possibly empty) array, not null")
	}
	if len(body.Servers) != 0 {
		t.Errorf("no servers configured → empty array, got %d", len(body.Servers))
	}
}

// TestDevOps_ServersRequiresAuth confirms the endpoint is auth-gated.
func TestDevOps_ServersRequiresAuth(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Get(p.APIAddr + "/api/devops/servers")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("servers without key = %d, want 401", resp.StatusCode)
	}
}
