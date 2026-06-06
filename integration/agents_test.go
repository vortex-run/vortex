//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// TestAgents_SubmitGeneralQuestion starts a real vortex process and submits a
// message to the coordinator, asserting a 200 with a non-empty response. The
// coordinator uses the stub AI gateway (no real provider in M10), so the reply
// is the canned stub response.
func TestAgents_SubmitGeneralQuestion(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	body := strings.NewReader(`{"message":"what is the capital of France?","session_id":"s1"}`)
	resp, err := http.Post(p.APIAddr+"/api/agents/submit", "application/json", body)
	if err != nil {
		t.Fatalf("POST submit: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		Response  string `json:"response"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Response == "" {
		t.Error("response field should be non-empty")
	}
}

// TestAgents_StatusEndpoint asserts the runtime status endpoint reports stats.
func TestAgents_StatusEndpoint(t *testing.T) {
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Get(p.APIAddr + "/api/agents/status")
	if err != nil {
		t.Fatalf("GET status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var stats map[string]any
	if err := json.Unmarshal(body, &stats); err != nil {
		t.Fatalf("decode stats: %v\n%s", err, body)
	}
	if _, ok := stats["active_agents"]; !ok {
		t.Errorf("status missing active_agents field: %s", body)
	}
}
