//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// TestOrchestrate_RequiresAuth confirms the orchestrate endpoint is auth-gated.
func TestOrchestrate_RequiresAuth(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Post(p.APIAddr+"/api/orchestrate", "application/json", strings.NewReader(`{"goal":"x"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("orchestrate without key = %d, want 401", resp.StatusCode)
	}
}

// TestOrchestrate_RouteExists confirms the endpoint is wired (400 missing goal /
// 503 no gateway — never 404).
func TestOrchestrate_RouteExists(t *testing.T) {
	secret := seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	req, _ := http.NewRequest(http.MethodPost, p.APIAddr+"/api/orchestrate", strings.NewReader(`{}`))
	req.Header.Set("X-API-Key", secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("orchestrate route status = %d, want a handled response (400/503)", resp.StatusCode)
	}
}
