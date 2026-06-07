//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/testutil"
)

// TestMessaging_WebhookEndpointsExist starts vortex with a Telegram bot
// configured and asserts the webhook endpoints are mounted (not 404). They are
// unauthenticated (no API key) but verify their own platform signature, so an
// empty/unsigned body is rejected with 4xx — crucially NOT 404.
func TestMessaging_WebhookEndpointsExist(t *testing.T) {
	t.Setenv("VORTEX_TELEGRAM_TOKEN", "123:FAKE")
	t.Setenv("VORTEX_TELEGRAM_DEFAULT_CHAT", "555")
	t.Setenv("VORTEX_TELEGRAM_SECRET", "s3cr3t")
	t.Setenv("VORTEX_SLACK_WEBHOOK", "http://127.0.0.1:1/none")
	t.Setenv("VORTEX_SLACK_SIGNING_SECRET", "sign")

	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	// Telegram webhook with no secret → 401 (registered, not 404).
	resp, err := http.Post(p.APIAddr+"/webhook/telegram", "application/json",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST telegram webhook: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Errorf("/webhook/telegram should be registered, got 404")
	}

	// Slack webhook with no signature → 401 (registered, not 404).
	resp2, err := http.Post(p.APIAddr+"/webhook/slack", "application/x-www-form-urlencoded",
		strings.NewReader("text=status"))
	if err != nil {
		t.Fatalf("POST slack webhook: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode == http.StatusNotFound {
		t.Errorf("/webhook/slack should be registered, got 404")
	}
}

// TestMessaging_AIGatewayStub starts vortex with NO AI keys and confirms the
// agent submit path still works using the stub coordinator (canned response).
func TestMessaging_AIGatewayStub(t *testing.T) {
	secret := seedAdminKey(t) // sets VORTEX_APIKEY_STORE before StartVortex
	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	req, _ := http.NewRequest(http.MethodPost, p.APIAddr+"/api/agents/submit",
		strings.NewReader(`{"message":"hello","session_id":"s1"}`))
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
}

// TestMessaging_WebhookUnknownPlatform404 confirms an unregistered webhook path
// returns 404.
func TestMessaging_WebhookUnknownPlatform404(t *testing.T) {
	t.Setenv("VORTEX_TELEGRAM_TOKEN", "123:FAKE")
	t.Setenv("VORTEX_TELEGRAM_DEFAULT_CHAT", "555")

	bin := getNetBinary(t)
	cfg := testutil.WriteTestConfig(t, netConfig(""))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	resp, err := http.Post(p.APIAddr+"/webhook/discord", "application/json",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown webhook = %d, want 404", resp.StatusCode)
	}
}
