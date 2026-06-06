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
// message to the coordinator (with a valid API key — agent endpoints require
// auth), asserting a 200 with a non-empty response. The coordinator uses the
// stub AI gateway (no real provider in M10), so the reply is the canned stub.
func TestAgents_SubmitGeneralQuestion(t *testing.T) {
	secret := seedAdminKey(t) // sets VORTEX_APIKEY_STORE before StartVortex
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	body := strings.NewReader(`{"message":"what is the capital of France?","session_id":"s1"}`)
	req, _ := http.NewRequest(http.MethodPost, p.APIAddr+"/api/agents/submit", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", secret)
	resp, err := http.DefaultClient.Do(req)
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

// TestAgents_SubmitRequiresAuth confirms the hardened behavior: the agent
// submit endpoint rejects an unauthenticated request even though it originates
// from localhost (no control-plane loopback bypass for the data plane).
func TestAgents_SubmitRequiresAuth(t *testing.T) {
	_ = seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Post(p.APIAddr+"/api/agents/submit", "application/json",
		strings.NewReader(`{"message":"x"}`))
	if err != nil {
		t.Fatalf("POST submit: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("submit without key = %d, want 401", resp.StatusCode)
	}
}

// TestAgents_StatusEndpoint asserts the runtime status endpoint reports stats
// (with a valid API key).
func TestAgents_StatusEndpoint(t *testing.T) {
	secret := seedAdminKey(t)
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	req, _ := http.NewRequest(http.MethodGet, p.APIAddr+"/api/agents/status", nil)
	req.Header.Set("X-API-Key", secret)
	resp, err := http.DefaultClient.Do(req)
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
