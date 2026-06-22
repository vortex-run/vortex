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

func TestTeam_A2AEndpointExists(t *testing.T) {
	p, _ := startTeamVortex(t)
	defer p.Stop(t)

	// /a2a/agents is loopback agent traffic — not auth-gated.
	resp, err := http.Get(p.APIAddr + "/a2a/agents")
	if err != nil {
		t.Fatalf("GET /a2a/agents: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var cards []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&cards); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cards) != 3 { // the 3 specialists are registered with the A2A server
		t.Errorf("/a2a/agents = %d agents, want 3", len(cards))
	}
}

func TestTeam_AgentCard(t *testing.T) {
	p, _ := startTeamVortex(t)
	defer p.Stop(t)

	resp, err := http.Get(p.APIAddr + "/a2a/agents/code-agent/card")
	if err != nil {
		t.Fatalf("GET card: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var card struct {
		Role         string   `json:"role"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if card.Role != "coder" {
		t.Errorf("code-agent role = %q, want coder", card.Role)
	}
	if len(card.Capabilities) == 0 {
		t.Error("code-agent should advertise capabilities")
	}
}
