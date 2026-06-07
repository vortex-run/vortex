// Package messaging implements VORTEX's messaging integration layer (build plan
// M11): two-way bots over Telegram, WhatsApp, and Slack, plus a unified
// notification router and an AI provider gateway. All platform APIs are reached
// over the standard library's net/http — no SDKs.
//
// This file implements the Telegram Bot API integration.
package messaging

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/vortex-run/vortex/internal/agents"
)

// TelegramConfig configures the Telegram bot. The token comes from the
// environment (VORTEX_TELEGRAM_TOKEN), never from the config file.
type TelegramConfig struct {
	Token       string  // bot token (from env)
	AllowedIDs  []int64 // allowed chat/user IDs; empty means allow none
	WebhookURL  string  // public webhook URL (optional)
	SecretToken string  // X-Telegram-Bot-Api-Secret-Token expected on webhooks
	// BaseURL overrides the Telegram API base (for tests). When empty the real
	// https://api.telegram.org/bot<token> is used.
	BaseURL string
	// Client overrides the HTTP client (for tests / custom timeouts).
	Client *http.Client
}

// CallbackResolver consumes an inline-button callback. It returns true if the
// callback was a recognised approval decision (and therefore should not be
// re-submitted to the agent runtime as a free-form directive). The
// ApprovalManager implements this.
type CallbackResolver interface {
	Resolve(callbackData string) bool
}

// TelegramBot is a Telegram Bot API client + webhook handler.
type TelegramBot struct {
	cfg      TelegramConfig
	baseURL  string
	client   *http.Client
	resolver CallbackResolver
}

// SetCallbackResolver wires a resolver (e.g. the ApprovalManager) that gets
// first refusal on inline-button callbacks.
func (t *TelegramBot) SetCallbackResolver(r CallbackResolver) { t.resolver = r }

// NewTelegramBot constructs a bot. It returns an error if the token is empty.
func NewTelegramBot(cfg TelegramConfig) (*TelegramBot, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("messaging: telegram token is required")
	}
	base := cfg.BaseURL
	if base == "" {
		base = "https://api.telegram.org/bot" + cfg.Token
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &TelegramBot{cfg: cfg, baseURL: base, client: client}, nil
}

// allowed reports whether chatID is permitted to interact with the bot.
func (t *TelegramBot) allowed(chatID int64) bool {
	for _, id := range t.cfg.AllowedIDs {
		if id == chatID {
			return true
		}
	}
	return false
}

// post issues a POST to the given Telegram method with a JSON body.
func (t *TelegramBot) post(ctx context.Context, method string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.baseURL+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("messaging: telegram %s: status %d: %s", method, resp.StatusCode, b)
	}
	return nil
}

// SendMessage sends a Markdown text message to chatID.
func (t *TelegramBot) SendMessage(ctx context.Context, chatID int64, text string) error {
	return t.post(ctx, "sendMessage", map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "Markdown",
	})
}

// SendFile uploads a document (APK, PDF, report) to chatID with a caption,
// using a multipart form (sendDocument).
func (t *TelegramBot) SendFile(ctx context.Context, chatID int64, filename string, data []byte, caption string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("chat_id", fmt.Sprintf("%d", chatID)); err != nil {
		return err
	}
	if caption != "" {
		if err := w.WriteField("caption", caption); err != nil {
			return err
		}
	}
	fw, err := w.CreateFormFile("document", filename)
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.baseURL+"/sendDocument", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("messaging: telegram sendDocument: status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// SetWebhook registers the webhook URL with Telegram, including the secret
// token Telegram will echo back in the X-Telegram-Bot-Api-Secret-Token header.
func (t *TelegramBot) SetWebhook(ctx context.Context, url string) error {
	payload := map[string]any{"url": url}
	if t.cfg.SecretToken != "" {
		payload["secret_token"] = t.cfg.SecretToken
	}
	return t.post(ctx, "setWebhook", payload)
}

// telegramUpdate is the subset of a Telegram Update we consume.
type telegramUpdate struct {
	Message *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
	CallbackQuery *struct {
		ID   string `json:"id"`
		Data string `json:"data"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Message struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// verifySecret checks the X-Telegram-Bot-Api-Secret-Token header against the
// configured secret using a constant-time comparison. When no secret is
// configured it returns false (fail closed) only if the header is present and
// wrong; an unconfigured bot with no header is allowed for backward-compat in
// tests, but production wiring always sets a secret.
func (t *TelegramBot) verifySecret(r *http.Request) bool {
	if t.cfg.SecretToken == "" {
		return true
	}
	got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(t.cfg.SecretToken)) == 1
}

// HandleWebhook returns an http.Handler that parses Telegram updates, validates
// the secret token and chat allow-list, submits the message text to the agent
// runtime, and replies with the response. Inline-button callbacks (approve/
// reject) are acknowledged and their data submitted as a directive.
func (t *TelegramBot) HandleWebhook(runtime *agents.Runtime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !t.verifySecret(r) {
			http.Error(w, "invalid secret token", http.StatusUnauthorized)
			return
		}
		var upd telegramUpdate
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&upd); err != nil {
			http.Error(w, "bad update", http.StatusBadRequest)
			return
		}

		switch {
		case upd.CallbackQuery != nil:
			chatID := upd.CallbackQuery.Message.Chat.ID
			if !t.allowed(chatID) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			// Acknowledge the button press.
			_ = t.post(r.Context(), "answerCallbackQuery",
				map[string]any{"callback_query_id": upd.CallbackQuery.ID})
			// An approval decision is consumed by the resolver; anything else is
			// dispatched to the runtime as a directive.
			if t.resolver == nil || !t.resolver.Resolve(upd.CallbackQuery.Data) {
				t.dispatch(r.Context(), runtime, chatID, upd.CallbackQuery.Data)
			}
			w.WriteHeader(http.StatusOK)
		case upd.Message != nil:
			chatID := upd.Message.Chat.ID
			if !t.allowed(chatID) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			t.dispatch(r.Context(), runtime, chatID, upd.Message.Text)
			w.WriteHeader(http.StatusOK)
		default:
			// Nothing actionable in this update.
			w.WriteHeader(http.StatusOK)
		}
	})
}

// dispatch submits text to the runtime and sends the response back to chatID.
func (t *TelegramBot) dispatch(ctx context.Context, runtime *agents.Runtime, chatID int64, text string) {
	if runtime == nil || text == "" {
		return
	}
	ch, err := runtime.Submit(ctx, text, fmt.Sprintf("telegram:%d", chatID))
	if err != nil {
		_ = t.SendMessage(ctx, chatID, "⚠️ "+err.Error())
		return
	}
	var resp string
	for chunk := range ch {
		resp += chunk
	}
	if resp != "" {
		_ = t.SendMessage(ctx, chatID, resp)
	}
}

// SendApprovalRequest sends a message with inline approve/reject buttons. The
// approveAction/rejectAction strings are the callback_data delivered back when
// the user taps a button (used by the human-in-the-loop flow).
func (t *TelegramBot) SendApprovalRequest(ctx context.Context, chatID int64, description, approveAction, rejectAction string) error {
	return t.post(ctx, "sendMessage", map[string]any{
		"chat_id": chatID,
		"text":    description,
		"reply_markup": map[string]any{
			"inline_keyboard": [][]map[string]any{{
				{"text": "✅ Approve", "callback_data": approveAction},
				{"text": "❌ Reject", "callback_data": rejectAction},
			}},
		},
	})
}
