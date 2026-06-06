package messaging

import (
	"context"
	"testing"
	"time"
)

func TestRouter_RoutesBySeverity(t *testing.T) {
	tg := newFakeTelegram(t)
	tgBot := newTestBot(t, tg, TelegramConfig{})
	sl := newFakeSlackWebhook(t)
	slBot, _ := NewSlackBot(SlackConfig{WebhookURL: sl.srv.URL, Client: sl.srv.Client()})

	r := NewRouter(NotificationConfig{
		Rules: []NotificationRule{
			{Severity: SeverityCritical, Channels: []string{ChannelTelegram, ChannelSlack}},
			{Severity: SeverityInfo, Channels: []string{ChannelSlack}},
		},
		Telegram:      tgBot,
		Slack:         slBot,
		DefaultChatID: 5,
	})

	// Critical → both telegram and slack.
	if err := r.Send(context.Background(), SeverityCritical, "down", "db unreachable"); err != nil {
		t.Fatalf("Send critical: %v", err)
	}
	if tg.method() != "sendMessage" {
		t.Errorf("telegram not notified on critical, method=%q", tg.method())
	}
	if sl.lastJSON["attachments"] == nil {
		t.Errorf("slack not notified on critical")
	}
}

func TestRouter_InfoGoesToSlackOnly(t *testing.T) {
	tg := newFakeTelegram(t)
	tgBot := newTestBot(t, tg, TelegramConfig{})
	sl := newFakeSlackWebhook(t)
	slBot, _ := NewSlackBot(SlackConfig{WebhookURL: sl.srv.URL, Client: sl.srv.Client()})

	r := NewRouter(NotificationConfig{
		Rules:    []NotificationRule{{Severity: SeverityInfo, Channels: []string{ChannelSlack}}},
		Telegram: tgBot,
		Slack:    slBot,
	})

	if err := r.Send(context.Background(), SeverityInfo, "fyi", "rolling restart"); err != nil {
		t.Fatalf("Send info: %v", err)
	}
	if sl.lastJSON["attachments"] == nil {
		t.Error("slack should receive info")
	}
	if tg.method() != "" {
		t.Errorf("telegram should NOT receive info, got method=%q", tg.method())
	}
}

func TestRouter_SkipsSilencedChannel(t *testing.T) {
	tg := newFakeTelegram(t)
	tgBot := newTestBot(t, tg, TelegramConfig{})

	r := NewRouter(NotificationConfig{
		Rules:         []NotificationRule{{Severity: SeverityCritical, Channels: []string{ChannelTelegram}}},
		Telegram:      tgBot,
		DefaultChatID: 1,
	})
	r.Silence(ChannelTelegram, time.Hour)

	if err := r.Send(context.Background(), SeverityCritical, "x", "y"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if tg.method() != "" {
		t.Errorf("silenced telegram should not be notified, got method=%q", tg.method())
	}
}

func TestRouter_RuleSilenceUntilSkips(t *testing.T) {
	tg := newFakeTelegram(t)
	tgBot := newTestBot(t, tg, TelegramConfig{})

	r := NewRouter(NotificationConfig{
		Rules: []NotificationRule{{
			Severity: SeverityCritical, Channels: []string{ChannelTelegram},
			SilenceUntil: time.Now().Add(time.Hour),
		}},
		Telegram:      tgBot,
		DefaultChatID: 1,
	})

	if err := r.Send(context.Background(), SeverityCritical, "x", "y"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if tg.method() != "" {
		t.Errorf("rule SilenceUntil should suppress delivery, got method=%q", tg.method())
	}
}

func TestRouter_NoChannelsReturnsNil(t *testing.T) {
	r := NewRouter(NotificationConfig{}) // no rules, no bots
	if err := r.Send(context.Background(), SeverityCritical, "x", "y"); err != nil {
		t.Errorf("Send with no channels should return nil, got %v", err)
	}
}

func TestRouter_SendFileToTelegram(t *testing.T) {
	tg := newFakeTelegram(t)
	tgBot := newTestBot(t, tg, TelegramConfig{})
	r := NewRouter(NotificationConfig{Telegram: tgBot, DefaultChatID: 9})

	if err := r.SendFile(context.Background(), "report.pdf", []byte("PDF"), "weekly report"); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if tg.method() != "sendDocument" {
		t.Errorf("SendFile method = %q, want sendDocument", tg.method())
	}
}

func TestRouter_ConfiguredChannels(t *testing.T) {
	tg := newFakeTelegram(t)
	tgBot := newTestBot(t, tg, TelegramConfig{})
	r := NewRouter(NotificationConfig{Telegram: tgBot})
	got := r.ConfiguredChannels()
	if len(got) != 1 || got[0] != ChannelTelegram {
		t.Errorf("ConfiguredChannels = %v, want [telegram]", got)
	}
}
