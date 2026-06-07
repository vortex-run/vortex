//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// TestStudio_GracefulDegradationWithoutCodeServer is the core M12 graceful-
// degradation test: code-server is not installed in CI, yet Studio must start,
// the server must not crash, and the non-IDE endpoints must work. This test
// MUST NOT skip.
func TestStudio_GracefulDegradationWithoutCodeServer(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VORTEX_STUDIO_WORKSPACE", wd) // the repo itself (a git repo)
	secret := seedAdminKey(t)

	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	// The server is up and healthy despite code-server being absent.
	resp, err := http.Get(p.APIAddr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health = %d, want 200 (server should not crash)", resp.StatusCode)
	}

	// Studio startup should be logged.
	if logs := p.Logs(); !contains(logs, "studio started") {
		t.Errorf("expected 'studio started' in logs:\n%s", logs)
	}

	// Git status works (workspace is a repo), with a valid key.
	req, _ := http.NewRequest(http.MethodGet, p.APIAddr+"/studio/git/status", nil)
	req.Header.Set("X-API-Key", secret)
	gitResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /studio/git/status: %v", err)
	}
	defer func() { _ = gitResp.Body.Close() }()
	if gitResp.StatusCode != http.StatusOK {
		t.Fatalf("/studio/git/status = %d, want 200", gitResp.StatusCode)
	}
	var status struct {
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(gitResp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Branch == "" {
		t.Error("git status should report a branch")
	}
}

// TestStudio_TerminalEndpointRequiresAuth confirms the terminal endpoint is
// mounted and rejects unauthenticated requests (no API key → 401, not 404).
func TestStudio_TerminalEndpointRequiresAuth(t *testing.T) {
	wd, _ := os.Getwd()
	t.Setenv("VORTEX_STUDIO_WORKSPACE", wd)
	_ = seedAdminKey(t)

	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Get(p.APIAddr + "/studio/terminal")
	if err != nil {
		t.Fatalf("GET /studio/terminal: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Errorf("/studio/terminal should be mounted, got 404")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/studio/terminal without key = %d, want 401", resp.StatusCode)
	}
}

// TestStudio_DBConnectionsList confirms the DB connections endpoint returns the
// mTLS TCP routes (read-only by default), with auth.
func TestStudio_DBConnectionsList(t *testing.T) {
	wd, _ := os.Getwd()
	t.Setenv("VORTEX_STUDIO_WORKSPACE", wd)
	secret := seedAdminKey(t)

	bin := getNetBinary(t)
	// A config with an mTLS TCP route so DB studio has a connection to list.
	routes := `{name: "pg", protocol: "tcp", listen: 15432, backends: [{host: "127.0.0.1", port: 5432}], mtls: true}`
	cfg := testutil.WriteTestConfig(t, netConfig(routes))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	req, _ := http.NewRequest(http.MethodGet, p.APIAddr+"/studio/db/connections", nil)
	req.Header.Set("X-API-Key", secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /studio/db/connections: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/studio/db/connections = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Connections []struct {
			Name string `json:"name"`
		} `json:"connections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The mTLS route "pg" should be listed as a DB connection.
	found := false
	for _, c := range body.Connections {
		if c.Name == "pg" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected mTLS route 'pg' in DB connections, got %+v", body.Connections)
	}
}
