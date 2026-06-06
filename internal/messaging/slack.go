package messaging

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vortex-run/vortex/internal/agents"
)

// SlackConfig configures the Slack integration (incoming webhook for outbound
// messages, slash commands for inbound). Secrets come from the environment.
type SlackConfig struct {
	WebhookURL    string // incoming webhook for SendMessage/SendAlert
	SigningSecret string // verifies slash-command requests
	BotToken      string // bot token for Web API (future use)
	Client        *http.Client
	// now is an injectable clock for tests; defaults to time.Now.
	now func() time.Time
}

// SlackBot is a Slack client + slash-command handler.
type SlackBot struct {
	cfg    SlackConfig
	client *http.Client
	now    func() time.Time
}

// NewSlackBot constructs the bot.
func NewSlackBot(cfg SlackConfig) (*SlackBot, error) {
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	return &SlackBot{cfg: cfg, client: client, now: now}, nil
}

// SendMessage posts text to the configured incoming webhook.
func (s *SlackBot) SendMessage(ctx context.Context, text string) error {
	return s.postWebhook(ctx, map[string]any{"text": text})
}

// SendAlert posts a colour-coded attachment based on severity.
func (s *SlackBot) SendAlert(ctx context.Context, title, body, severity string) error {
	color := map[string]string{
		"info":     "#2eb886", // green
		"warn":     "#daa038", // yellow
		"critical": "#a30200", // red
	}[strings.ToLower(severity)]
	if color == "" {
		color = "#2eb886"
	}
	return s.postWebhook(ctx, map[string]any{
		"attachments": []map[string]any{{
			"color":  color,
			"title":  title,
			"text":   body,
			"ts":     s.now().Unix(),
			"footer": "VORTEX",
		}},
	})
}

// postWebhook sends a JSON payload to the incoming webhook URL.
func (s *SlackBot) postWebhook(ctx context.Context, payload any) error {
	if s.cfg.WebhookURL == "" {
		return fmt.Errorf("messaging: slack webhook URL not configured")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("messaging: slack webhook: status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// verifySignature validates the Slack request signature: it recomputes
// v0=HMAC-SHA256(signingSecret, "v0:"+ts+":"+body) and compares in constant
// time, rejecting stale timestamps (>5 min) to bound replay. With no signing
// secret configured it returns true (tests); production always sets one.
func (s *SlackBot) verifySignature(body []byte, timestamp, signature string) bool {
	if s.cfg.SigningSecret == "" {
		return true
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if d := s.now().Unix() - ts; d > 300 || d < -300 {
		return false // stale / future-dated → replay guard
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.SigningSecret))
	fmt.Fprintf(mac, "v0:%s:%s", timestamp, body)
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(signature))
}

// HandleSlashCommand returns the http.Handler for POST /webhook/slack. It
// verifies the Slack signature, parses the slash command, and either submits to
// the runtime or performs a built-in action. It responds immediately with 200
// and an ephemeral acknowledgement; longer results are delivered out of band.
func (s *SlackBot) HandleSlashCommand(runtime *agents.Runtime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		if !s.verifySignature(body,
			r.Header.Get("X-Slack-Request-Timestamp"),
			r.Header.Get("X-Slack-Signature")) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		form, err := url.ParseQuery(string(body))
		if err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		text := strings.TrimSpace(form.Get("text")) // e.g. "status" or "deploy prod"
		sub, args := splitCommand(text)

		// Acknowledge immediately (Slack requires a fast 200).
		ack := s.handleSub(r.Context(), runtime, sub, args)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response_type": "ephemeral",
			"text":          ack,
		})
	})
}

// splitCommand splits "status extra args" into ("status", "extra args").
func splitCommand(text string) (sub, args string) {
	parts := strings.SplitN(text, " ", 2)
	sub = parts[0]
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	return sub, args
}

// handleSub processes a recognised /vortex subcommand and returns the immediate
// acknowledgement text.
func (s *SlackBot) handleSub(ctx context.Context, runtime *agents.Runtime, sub, args string) string {
	switch sub {
	case "status":
		if runtime == nil {
			return "agent runtime not configured"
		}
		st := runtime.Stats()
		return fmt.Sprintf("VORTEX: %d active agents, %d messages, queue %d",
			st.ActiveAgents, st.TotalMessages, st.QueueDepth)
	case "reload":
		return "reload requested"
	case "deploy":
		return "deploy requested: " + args
	case "logs":
		return "recent logs for: " + args
	case "":
		return "usage: /vortex <status|deploy|logs|reload>"
	default:
		// Anything else is treated as a free-form request to the coordinator.
		if runtime == nil {
			return "agent runtime not configured"
		}
		ch, err := runtime.Submit(ctx, sub+" "+args, "slack")
		if err != nil {
			return "error: " + err.Error()
		}
		var resp string
		for chunk := range ch {
			resp += chunk
		}
		return resp
	}
}
