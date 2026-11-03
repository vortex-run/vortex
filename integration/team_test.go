//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// startTeamVortex starts a --team server with an isolated config home and an
// admin key, returning the process and the admin secret.
func startTeamVortex(t *testing.T) (*testutil.VortexProcess, string) {
	t.Helper()
	adminSecret := seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortexWithArgs(t, bin, cfg, "--team")
	return p, adminSecret
}

func TestTeam_AgentsRegistered(t *testing.T) {
	p, secret := startTeamVortex(t)
	defer p.Stop(t)

	req, _ := http.NewRequest(http.MethodGet, p.APIAddr+"/api/team/agents", nil)
	req.Header.Set("X-API-Key", secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/team/agents: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (body=%s)", resp.StatusCode, body)
	}
	var out struct {
		Agents []struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	roles := map[string]string{}
	for _, a := range out.Agents {
		roles[a.ID] = a.Role
	}
	for id, wantRole := range map[string]string{
		"coordinator": "coordinator", "code-agent": "coder",
		"test-agent": "tester", "review-agent": "reviewer",
	} {
		if roles[id] != wantRole {
			t.Errorf("agent %s role = %q, want %q (all: %v)", id, roles[id], wantRole, roles)
		}
	}
}

// /a2a/ is the in-process agents' private channel: RPC submits tasks that
// write files through the approval-free trusted registry. Only the
// coordinator's per-process token opens it. Anonymous callers, forged tokens
// and even valid API keys (admin included) are refused.
func TestTeam_A2ARequiresToken(t *testing.T) {
	p, secret := startTeamVortex(t)
	defer p.Stop(t)

	cases := []struct {
		name, method, path string
		header             map[string]string
	}{
		{"anonymous list", http.MethodGet, "/a2a/agents", nil},
		{"anonymous card", http.MethodGet, "/a2a/agents/code-agent/card", nil},
		{"anonymous rpc", http.MethodPost, "/a2a/agents/code-agent/rpc", nil},
		{"forged token", http.MethodGet, "/a2a/agents", map[string]string{"X-A2A-Token": "guess"}},
		{"admin API key", http.MethodPost, "/a2a/agents/code-agent/rpc", map[string]string{"X-API-Key": secret}},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, p.APIAddr+c.path, nil)
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %s %s = %d, want 401", c.name, c.method, c.path, resp.StatusCode)
		}
	}
}

// The agents' cards are available to API clients through the authenticated
// catalog instead of the private /a2a/ tree.
func TestTeam_CatalogAdvertisesCoder(t *testing.T) {
	p, secret := startTeamVortex(t)
	defer p.Stop(t)

	req, _ := http.NewRequest(http.MethodGet, p.APIAddr+"/api/agents/catalog", nil)
	req.Header.Set("X-API-Key", secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET catalog: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Agents []struct {
			Role         string   `json:"role"`
			Capabilities []string `json:"capabilities"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	for _, a := range out.Agents {
		if a.Role == "coder" {
			if len(a.Capabilities) == 0 {
				t.Error("coder should advertise capabilities")
			}
			return
		}
	}
	t.Errorf("no coder in catalog: %+v", out.Agents)
}
