//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// TestForge_EndpointsRequireAuth confirms the forge endpoints are mounted and
// reject unauthenticated requests (401, not 404).
func TestForge_EndpointsRequireAuth(t *testing.T) {
	_ = seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Post(p.APIAddr+"/api/forge/build", "application/json",
		strings.NewReader(`{"message":"build a go program"}`))
	if err != nil {
		t.Fatalf("POST /api/forge/build: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Errorf("/api/forge/build should be mounted, got 404")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("forge build without key = %d, want 401", resp.StatusCode)
	}
}

// TestForge_DisabledWithoutAIGateway confirms graceful degradation: with no AI
// provider configured, Forge is disabled and the endpoint returns 503 (not a
// crash, not 404). VORTEX Forge requires an AI gateway for code generation, so
// CI (which has no provider keys) exercises this degraded path.
func TestForge_DisabledWithoutAIGateway(t *testing.T) {
	secret := seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	req, _ := http.NewRequest(http.MethodGet, p.APIAddr+"/api/forge/jobs", nil)
	req.Header.Set("X-API-Key", secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/forge/jobs: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("forge disabled (no AI key) should return 503, got %d", resp.StatusCode)
	}

	// The server must remain healthy despite forge being disabled.
	h, err := http.Get(p.APIAddr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	_ = h.Body.Close()
	if h.StatusCode != http.StatusOK {
		t.Errorf("/health = %d, want 200 (server must not crash)", h.StatusCode)
	}
}
