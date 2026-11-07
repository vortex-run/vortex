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
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sandeepbollavaram/vortex/internal/agents"
)

// SlackConfig configures the Slack integration (incoming webhook for outbound
// messages, slash commands for inbound). Secrets come from the environment.
type SlackConfig struct {
	WebhookURL    string // incoming webhook for SendMessage/SendAlert
	SigningSecret string // verifies slash-command requests
	BotToken      string // bot token for Web API (future use)
	// AllowedUsers are the registered operators: full access (agent
	// requests, deploy, reload). Entries are Slack user IDs ("U012AB") or
	// team-scoped ("T012AB:U012AB"). Empty with ReadOnlyUsers empty means
	// nobody can command the bot — only `whoami` answers.
	AllowedUsers []string
	// ReadOnlyUsers may run status, logs and help only.
	ReadOnlyUsers []string
	// AllowedTeams, when set, restricts every user to these workspaces.
	AllowedTeams []string
	Client       *http.Client
	// now is an injectable clock for tests; defaults to time.Now.
	now func() time.Time
}

// InboundEnabled reports whether slash commands can be authenticated. Outbound
// alerts only need WebhookURL; the inbound route needs the signing secret, so
// the caller must not mount it without one.
func (c SlackConfig) InboundEnabled() bool { return c.SigningSecret != "" }

// InboundEnabled reports whether this bot's slash-command route can authenticate.
func (s *SlackBot) InboundEnabled() bool { return s.cfg.InboundEnabled() }

// SlackBot is a Slack client + slash-command handler.
type SlackBot struct {
	cfg    SlackConfig
	client *http.Client
	now    func() time.Time
	allow  slackAllowList
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
	return &SlackBot{cfg: cfg, client: client, now: now,
		allow: newSlackAllowList(cfg.AllowedTeams, cfg.AllowedUsers, cfg.ReadOnlyUsers)}, nil
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
			"footer": "Gubern",
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
// time, rejecting stale timestamps (>5 min) to bound replay. It fails closed:
// with no signing secret nothing can be verified, so nothing is accepted. The
// slash-command route is public (no API key) and feeds the agent runtime.
func (s *SlackBot) verifySignature(body []byte, timestamp, signature string) bool {
	if s.cfg.SigningSecret == "" {
		return false
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

		// One agent session per signed Slack principal (team + user), like
		// telegram:<chat> and whatsapp:<number>. A shared session fed every
		// workspace member's history into everyone else's replies (audit F-07).
		teamID, userID := form.Get("team_id"), form.Get("user_id")
		session := slackSessionID(teamID, userID)

		// Signed by Slack is not the same as trusted by the operator: every
		// command but whoami/help needs a registered user.
		sub = strings.ToLower(sub)
		var ack string
		have, need := s.allow.access(teamID, userID), requiredAccess(sub)
		switch {
		case sub == "whoami":
			ack = s.whoami(teamID, userID, have)
		case have < need:
			slog.Warn("slack: command refused for unregistered or read-only user",
				"team", teamID, "user", userID, "command", sub)
			ack = s.refusal(have)
		default:
			// Acknowledge immediately (Slack requires a fast 200).
			ack = s.handleSub(r.Context(), runtime, session, sub, args)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response_type": "ephemeral",
			"text":          ack,
		})
	})
}

// slackSessionID builds the per-principal agent session for a signed Slack
// request, "slack-<team>-<user>". Slack IDs are short alphanumerics; anything
// else (or a missing id) yields "" so the caller refuses rather than falling
// back to a shared session.
func slackSessionID(teamID, userID string) string {
	valid := func(id string) bool {
		if id == "" || len(id) > 32 {
			return false
		}
		for _, c := range id {
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
				return false
			}
		}
		return true
	}
	if !valid(teamID) || !valid(userID) {
		return ""
	}
	return "slack-" + teamID + "-" + userID
}

// whoami tells a person their Slack IDs and access level, so an operator can
// register them. It reveals only the caller's own identifiers.
func (s *SlackBot) whoami(teamID, userID string, have SlackAccess) string {
	level := map[SlackAccess]string{SlackNone: "not registered", SlackReadOnly: "read-only", SlackOperator: "operator"}[have]
	return fmt.Sprintf("team_id=%s user_id=%s access=%s\nTo register: add %s:%s to GUBERN_SLACK_ALLOWED_USERS (or GUBERN_SLACK_READONLY_USERS) and restart Gubern.",
		teamID, userID, level, teamID, userID)
}

// refusal explains why a command was refused.
func (s *SlackBot) refusal(have SlackAccess) string {
	if have == SlackReadOnly {
		return "You have read-only access: status, logs and help only."
	}
	if !s.allow.configured() {
		return "No Slack users are registered with this Gubern server yet. Run /vortex whoami and give the IDs to the operator."
	}
	return "You are not registered with this Gubern server. Run /vortex whoami and give the IDs to the operator."
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
func (s *SlackBot) handleSub(ctx context.Context, runtime *agents.Runtime, session, sub, args string) string {
	switch sub {
	case "status":
		if runtime == nil {
			return "agent runtime not configured"
		}
		st := runtime.Stats()
		return fmt.Sprintf("Gubern: %d active agents, %d messages, queue %d",
			st.ActiveAgents, st.TotalMessages, st.QueueDepth)
	case "reload":
		return "reload requested"
	case "deploy":
		return "deploy requested: " + args
	case "logs":
		return "recent logs for: " + args
	case "", "help":
		return "usage: /vortex <status|logs|deploy|reload|whoami> or /vortex <request for the agent>"
	default:
		// Anything else is treated as a free-form request to the coordinator.
		if runtime == nil {
			return "agent runtime not configured"
		}
		if session == "" {
			return "error: request carries no Slack team/user identity"
		}
		ch, err := runtime.Submit(ctx, sub+" "+args, session)
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
