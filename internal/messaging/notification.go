package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Severity classifies a notification's importance.
type Severity string

// Severity levels.
const (
	SeverityInfo     Severity = "info"
	SeverityWarn     Severity = "warn"
	SeverityCritical Severity = "critical"
)

// Channel names.
const (
	ChannelTelegram = "telegram"
	ChannelWhatsApp = "whatsapp"
	ChannelSlack    = "slack"
	ChannelEmail    = "email"
)

// NotificationRule maps a severity to the channels it should reach.
type NotificationRule struct {
	Severity     Severity
	Channels     []string
	SilenceUntil time.Time
}

// NotificationConfig configures the router. Any bot may be nil (not configured).
type NotificationConfig struct {
	Rules         []NotificationRule
	Telegram      *TelegramBot
	WhatsApp      *WhatsAppBot
	Slack         *SlackBot
	DefaultChatID int64  // Telegram default recipient
	DefaultPhone  string // WhatsApp default recipient
}

// Router dispatches notifications to configured channels per severity rules.
type Router struct {
	cfg NotificationConfig

	mu       sync.Mutex
	silenced map[string]time.Time // channel → silenced-until
	now      func() time.Time
}

// NewRouter builds a router from cfg.
func NewRouter(cfg NotificationConfig) *Router {
	return &Router{cfg: cfg, silenced: make(map[string]time.Time), now: time.Now}
}

// channelsFor returns the set of channels that should receive a notification of
// the given severity, honouring per-rule SilenceUntil.
func (r *Router) channelsFor(sev Severity) []string {
	seen := map[string]bool{}
	var out []string
	now := r.now()
	for _, rule := range r.cfg.Rules {
		if rule.Severity != sev {
			continue
		}
		if !rule.SilenceUntil.IsZero() && now.Before(rule.SilenceUntil) {
			continue
		}
		for _, c := range rule.Channels {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// silencedNow reports whether channel is currently silenced via Silence().
func (r *Router) silencedNow(channel string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.silenced[channel]
	return ok && r.now().Before(until)
}

// Send dispatches a notification to every channel matching the severity rules,
// skipping silenced channels. It returns nil when there is nothing to send (no
// matching rule or no configured channel) and a combined error if any send
// fails.
func (r *Router) Send(ctx context.Context, severity Severity, title, body string) error {
	var errs []error
	for _, ch := range r.channelsFor(severity) {
		if r.silencedNow(ch) {
			continue
		}
		if err := r.sendTo(ctx, ch, severity, title, body); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ch, err))
		}
	}
	return errors.Join(errs...)
}

// sendTo delivers a single notification to one channel.
func (r *Router) sendTo(ctx context.Context, channel string, severity Severity, title, body string) error {
	text := title
	if body != "" {
		text += "\n" + body
	}
	switch channel {
	case ChannelTelegram:
		if r.cfg.Telegram == nil {
			return nil
		}
		return r.cfg.Telegram.SendMessage(ctx, r.cfg.DefaultChatID, text)
	case ChannelWhatsApp:
		if r.cfg.WhatsApp == nil {
			return nil
		}
		return r.cfg.WhatsApp.SendText(ctx, r.cfg.DefaultPhone, text)
	case ChannelSlack:
		if r.cfg.Slack == nil {
			return nil
		}
		return r.cfg.Slack.SendAlert(ctx, title, body, string(severity))
	default:
		return nil // unknown / unconfigured channel (e.g. email) is a no-op
	}
}

// Silence suppresses a channel for the given duration.
func (r *Router) Silence(channel string, duration time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.silenced[channel] = r.now().Add(duration)
}

// SendFile delivers a file to channels that support file attachments
// (currently Telegram). It is a no-op for channels that don't.
func (r *Router) SendFile(ctx context.Context, filename string, data []byte, caption string) error {
	var errs []error
	if r.cfg.Telegram != nil {
		if err := r.cfg.Telegram.SendFile(ctx, r.cfg.DefaultChatID, filename, data, caption); err != nil {
			errs = append(errs, fmt.Errorf("telegram: %w", err))
		}
	}
	return errors.Join(errs...)
}

// ConfiguredChannels returns the names of channels that have a backing bot, for
// startup logging.
func (r *Router) ConfiguredChannels() []string {
	var out []string
	if r.cfg.Telegram != nil {
		out = append(out, ChannelTelegram)
	}
	if r.cfg.WhatsApp != nil {
		out = append(out, ChannelWhatsApp)
	}
	if r.cfg.Slack != nil {
		out = append(out, ChannelSlack)
	}
	return out
}

// String renders a severity for logging.
func (s Severity) String() string { return strings.ToUpper(string(s)) }
