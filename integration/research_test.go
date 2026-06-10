//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// TestResearch_ReportsEndpoint confirms the report list endpoint is wired and
// auth-gated.
func TestResearch_ReportsEndpoint(t *testing.T) {
	secret := seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	req, _ := http.NewRequest(http.MethodGet, p.APIAddr+"/api/research/reports", nil)
	req.Header.Set("X-API-Key", secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/research/reports: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/research/reports = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Reports []any `json:"reports"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// A fresh server has no reports yet — the field must still be a JSON array.
	if body.Reports == nil {
		t.Error("reports should be a (possibly empty) array")
	}
}

// TestResearch_ReportsRequiresAuth confirms the endpoint rejects unauthenticated
// requests (no localhost bypass).
func TestResearch_ReportsRequiresAuth(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Get(p.APIAddr + "/api/research/reports")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("reports without key = %d, want 401", resp.StatusCode)
	}
}
