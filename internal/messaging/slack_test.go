package messaging

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSlackWebhook captures the JSON posted to a Slack incoming webhook.
type fakeSlackWebhook struct {
	srv      *httptest.Server
	mu       sync.Mutex
	lastJSON map[string]any
}

func newFakeSlackWebhook(t *testing.T) *fakeSlackWebhook {
	t.Helper()
	f := &fakeSlackWebhook{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lastJSON = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&f.lastJSON)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestSlack_SendMessage(t *testing.T) {
	f := newFakeSlackWebhook(t)
	bot, _ := NewSlackBot(SlackConfig{WebhookURL: f.srv.URL, Client: f.srv.Client()})
	if err := bot.SendMessage(context.Background(), "deploy finished"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if f.lastJSON["text"] != "deploy finished" {
		t.Errorf("text = %v, want 'deploy finished'", f.lastJSON["text"])
	}
}

func TestSlack_SendAlertSeverityColor(t *testing.T) {
	f := newFakeSlackWebhook(t)
	bot, _ := NewSlackBot(SlackConfig{WebhookURL: f.srv.URL, Client: f.srv.Client()})
	if err := bot.SendAlert(context.Background(), "DB down", "primary unreachable", "critical"); err != nil {
		t.Fatalf("SendAlert: %v", err)
	}
	atts, ok := f.lastJSON["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("attachments = %v", f.lastJSON["attachments"])
	}
	att := atts[0].(map[string]any)
	if att["color"] != "#a30200" {
		t.Errorf("critical color = %v, want #a30200 (red)", att["color"])
	}
	if att["title"] != "DB down" {
		t.Errorf("title = %v", att["title"])
	}
}

// slackSign computes a valid Slack signature for body at timestamp ts.
func slackSign(secret, ts, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v0:%s:%s", ts, body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func slashRequest(t *testing.T, secret, command string, fixedNow time.Time) *http.Request {
	t.Helper()
	form := url.Values{"command": {"/vortex"}, "text": {command}}.Encode()
	ts := strconv.FormatInt(fixedNow.Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/webhook/slack", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", slackSign(secret, ts, form))
	return req
}

func newSignedSlackBot(t *testing.T, secret string, fixedNow time.Time) *SlackBot {
	t.Helper()
	bot, _ := NewSlackBot(SlackConfig{
		SigningSecret: secret,
		now:           func() time.Time { return fixedNow },
	})
	return bot
}

func TestSlack_SlashVerifiesSignature(t *testing.T) {
	now := time.Now()
	bot := newSignedSlackBot(t, "shh", now)
	rt := startStubRuntime(t)

	req := slashRequest(t, "shh", "status", now)
	rec := httptest.NewRecorder()
	bot.HandleSlashCommand(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("valid signature status = %d, want 200", rec.Code)
	}
}

func TestSlack_SlashRejectsInvalidSignature(t *testing.T) {
	now := time.Now()
	bot := newSignedSlackBot(t, "shh", now)
	rt := startStubRuntime(t)

	req := slashRequest(t, "WRONG-SECRET", "status", now)
	rec := httptest.NewRecorder()
	bot.HandleSlashCommand(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("invalid signature status = %d, want 401", rec.Code)
	}
}

func TestSlash_RejectsStaleTimestamp(t *testing.T) {
	now := time.Now()
	bot := newSignedSlackBot(t, "shh", now)
	rt := startStubRuntime(t)

	// Sign with a timestamp 10 minutes in the past — outside the replay window.
	stale := now.Add(-10 * time.Minute)
	req := slashRequest(t, "shh", "status", stale)
	rec := httptest.NewRecorder()
	bot.HandleSlashCommand(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("stale timestamp status = %d, want 401", rec.Code)
	}
}

func TestSlack_SlashStatus(t *testing.T) {
	now := time.Now()
	bot := newSignedSlackBot(t, "shh", now)
	rt := startStubRuntime(t)

	req := slashRequest(t, "shh", "status", now)
	rec := httptest.NewRecorder()
	bot.HandleSlashCommand(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "active agents") {
		t.Errorf("status response = %s, want agent stats", body)
	}
}

func TestSlack_SlashReload(t *testing.T) {
	now := time.Now()
	bot := newSignedSlackBot(t, "shh", now)
	rt := startStubRuntime(t)

	req := slashRequest(t, "shh", "reload", now)
	rec := httptest.NewRecorder()
	bot.HandleSlashCommand(rt).ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK || !strings.Contains(string(body), "reload requested") {
		t.Errorf("reload response: code=%d body=%s", rec.Code, body)
	}
}

func TestSlack_SlashRespondsImmediately(t *testing.T) {
	now := time.Now()
	bot := newSignedSlackBot(t, "shh", now)
	rt := startStubRuntime(t)

	req := slashRequest(t, "shh", "deploy prod", now)
	rec := httptest.NewRecorder()
	bot.HandleSlashCommand(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if resp["response_type"] != "ephemeral" {
		t.Errorf("response_type = %v, want ephemeral", resp["response_type"])
	}
}
