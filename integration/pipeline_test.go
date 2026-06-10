//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// TestPipeline_AnalyzeRequiresAuth confirms the analyze endpoint is auth-gated.
func TestPipeline_AnalyzeRequiresAuth(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Post(p.APIAddr+"/api/pipeline/analyze", "application/json", strings.NewReader(`{"request":"x","data":"a,b\n1,2\n"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("analyze without key = %d, want 401", resp.StatusCode)
	}
}

// TestPipeline_AnalyzeValidatesBody confirms the endpoint rejects an empty body
// (when wired) or returns 503 (when no AI gateway). Either is acceptable; the
// point is the route exists and is auth-gated.
func TestPipeline_AnalyzeRouteExists(t *testing.T) {
	secret := seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	req, _ := http.NewRequest(http.MethodPost, p.APIAddr+"/api/pipeline/analyze", strings.NewReader(`{}`))
	req.Header.Set("X-API-Key", secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// 400 (missing request) when wired, or 503 when no gateway — never 404/401.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("analyze route status = %d, want a handled response (400/503)", resp.StatusCode)
	}
}
