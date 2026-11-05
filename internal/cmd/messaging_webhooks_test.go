package cmd

import (
	"io"
	"log/slog"
	"testing"

	"github.com/sandeepbollavaram/vortex/internal/messaging"
)

func webhookPaths(t *testing.T, m messagingComponents) map[string]bool {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	paths := map[string]bool{}
	for _, s := range messagingWebhookSpecs(m, nil, log) {
		paths[s.Path] = true
	}
	return paths
}

// The WhatsApp and Slack inbound routes are public and feed the agent runtime,
// so they must not be mounted when their platform secret is missing, even
// though the bots themselves (outbound alerts) are configured.
func TestMessagingWebhookSpecs_InboundNeedsSecret(t *testing.T) {
	wa, err := messaging.NewWhatsAppBot(messaging.WhatsAppConfig{PhoneNumberID: "1", AccessToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	sl, err := messaging.NewSlackBot(messaging.SlackConfig{WebhookURL: "https://hooks.example.invalid/x"})
	if err != nil {
		t.Fatal(err)
	}
	paths := webhookPaths(t, messagingComponents{whatsapp: wa, slack: sl})
	if paths["/webhook/whatsapp"] {
		t.Error("/webhook/whatsapp mounted without GUBERN_WA_APP_SECRET")
	}
	if paths["/webhook/slack"] {
		t.Error("/webhook/slack mounted without GUBERN_SLACK_SIGNING_SECRET")
	}
}

func TestMessagingWebhookSpecs_InboundWithSecret(t *testing.T) {
	wa, err := messaging.NewWhatsAppBot(messaging.WhatsAppConfig{PhoneNumberID: "1", AccessToken: "tok", AppSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	sl, err := messaging.NewSlackBot(messaging.SlackConfig{WebhookURL: "https://hooks.example.invalid/x", SigningSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	paths := webhookPaths(t, messagingComponents{whatsapp: wa, slack: sl})
	if !paths["/webhook/whatsapp"] || !paths["/webhook/slack"] {
		t.Errorf("inbound routes missing with secrets configured: %v", paths)
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" 15550100000, ,+44 20 7946 0000 ,")
	if len(got) != 2 || got[0] != "15550100000" || got[1] != "+44 20 7946 0000" {
		t.Errorf("splitCSV = %q", got)
	}
	if splitCSV("") != nil {
		t.Error("splitCSV(\"\") should be nil")
	}
}
