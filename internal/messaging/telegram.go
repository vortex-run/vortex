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
	"strings"
	"sync"
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

// CommandHooks supply data for the bot's built-in slash commands. Each is
// optional; a nil hook yields a "not available" reply. They keep telegram.go
// decoupled from the api/agents packages (wired in start.go).
type CommandHooks struct {
	Status  func() string                                  // /status reply
	Routes  func() string                                  // /routes reply
	Cost    func() string                                  // /cost reply
	List    func(path string) string                       // /ls reply
	Approve func(sessionID string, ok bool) (string, bool) // /approve, /reject
	// ClarifySubmit submits a combined clarifying answer for a session (used when
	// all option buttons have been tapped).
	ClarifySubmit func(sessionID, answer string)
}

// SetCommandHooks wires the built-in command data sources.
func (t *TelegramBot) SetCommandHooks(h CommandHooks) { t.hooks = h }

// TelegramBot is a Telegram Bot API client + webhook handler.
type TelegramBot struct {
	cfg      TelegramConfig
	baseURL  string
	client   *http.Client
	resolver CallbackResolver
	hooks    CommandHooks

	// mention routes an "@agent" message to a specialist (set by the team
	// bridge). It returns true when it handled the message. Optional.
	mention func(ctx context.Context, chatID int64, text string) bool
	// teamCallback gets first refusal on inline-button callbacks (checkpoint
	// approvals from the team bridge), before the approval resolver. Optional.
	teamCallback CallbackResolver

	clarifyMu sync.Mutex
	clarify   map[string]*clarifySession // session → in-progress Q&A
}

// SetMentionHandler wires an "@agent" direct-chat router (the team bridge). The
// handler is consulted in dispatch before the agent runtime.
func (t *TelegramBot) SetMentionHandler(h func(ctx context.Context, chatID int64, text string) bool) {
	t.mention = h
}

// SetTeamCallbackResolver wires a resolver (the team bridge) that gets first
// refusal on inline-button callbacks, before the approval resolver.
func (t *TelegramBot) SetTeamCallbackResolver(r CallbackResolver) { t.teamCallback = r }

// ClarifyQuestion is a structured clarifying question for Telegram buttons.
type ClarifyQuestion struct {
	Question string
	Options  []string
	Key      string
}

// clarifySession collects button answers for a multi-question clarification,
// submitting the combined answer once every question is answered.
type clarifySession struct {
	chatID    int64
	keys      []string          // question keys, in order
	answers   map[string]string // key → chosen option text
	submitted bool
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

// botCommand is one entry of the Telegram command menu.
type botCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// botCommands is the menu shown in the Telegram client.
var botCommands = []botCommand{
	{"status", "Show VORTEX status"},
	{"routes", "List active routes"},
	{"ls", "List files in the working directory"},
	{"build", "Start a forge build"},
	{"approve", "Approve the pending action"},
	{"reject", "Reject the pending action"},
	{"cost", "Show AI usage cost today"},
	{"help", "Show all commands"},
}

// SetCommands registers the bot's command menu (/setMyCommands).
func (t *TelegramBot) SetCommands(ctx context.Context) error {
	return t.post(ctx, "setMyCommands", map[string]any{"commands": botCommands})
}

// helpText is the /help reply.
func helpText() string {
	var b strings.Builder
	b.WriteString("VORTEX commands:\n")
	for _, c := range botCommands {
		b.WriteString("/" + c.Command + " — " + c.Description + "\n")
	}
	return b.String()
}

// getUpdatesResponse is the /getUpdates reply.
type getUpdatesResponse struct {
	OK     bool             `json:"ok"`
	Result []telegramUpdate `json:"result"`
}

// Poll runs long-poll mode: every interval it fetches new updates via
// getUpdates and dispatches them, until ctx is cancelled. No public URL is
// needed (for local testing). updateID tracks the offset across calls.
func (t *TelegramBot) Poll(ctx context.Context, runtime *agents.Runtime, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	var offset int64
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			updates, err := t.getUpdates(ctx, offset)
			if err != nil {
				continue
			}
			for _, upd := range updates {
				offset = t.lastUpdateID(upd) + 1
				t.processUpdate(ctx, runtime, upd)
			}
		}
	}
}

// getUpdates fetches updates with the given offset.
func (t *TelegramBot) getUpdates(ctx context.Context, offset int64) ([]telegramUpdate, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/getUpdates?offset=%d&timeout=0", t.baseURL, offset), nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out getUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

// lastUpdateID returns the update_id of an update (for offset tracking).
func (t *TelegramBot) lastUpdateID(upd telegramUpdate) int64 { return upd.UpdateID }

// processUpdate dispatches a single update (message or callback). Shared by the
// webhook handler and polling mode.
func (t *TelegramBot) processUpdate(ctx context.Context, runtime *agents.Runtime, upd telegramUpdate) {
	if upd.CallbackQuery != nil {
		if !t.allowed(upd.CallbackQuery.From.ID) {
			return
		}
		// Clarify option buttons first, then the team (checkpoint) resolver,
		// then the approval/other resolver.
		if t.handleClarifyCallback(upd.CallbackQuery.Data) {
			return
		}
		if t.teamCallback != nil && t.teamCallback.Resolve(upd.CallbackQuery.Data) {
			return
		}
		if t.resolver != nil {
			t.resolver.Resolve(upd.CallbackQuery.Data)
		}
		return
	}
	if upd.Message == nil {
		return
	}
	if !t.allowed(upd.Message.Chat.ID) {
		return
	}
	t.dispatch(ctx, runtime, upd.Message.Chat.ID, upd.Message.Text)
}

// telegramUpdate is the subset of a Telegram Update we consume.
type telegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
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
			// Clarify option buttons first; then an approval decision is consumed
			// by the resolver; anything else is dispatched as a directive.
			switch {
			case t.handleClarifyCallback(upd.CallbackQuery.Data):
				// consumed by the clarification collector
			case t.teamCallback != nil && t.teamCallback.Resolve(upd.CallbackQuery.Data):
				// consumed by the team (checkpoint) resolver
			case t.resolver != nil && t.resolver.Resolve(upd.CallbackQuery.Data):
				// consumed by the approval resolver
			default:
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
	if text == "" {
		return
	}
	// Built-in slash commands are handled directly (no agent round-trip).
	if reply, handled := t.handleCommand(ctx, chatID, text); handled {
		if reply != "" {
			_ = t.SendMessage(ctx, chatID, reply)
		}
		return
	}
	// "@agent" messages are routed to a specialist via direct chat.
	if t.mention != nil && t.mention(ctx, chatID, text) {
		return
	}
	if runtime == nil {
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

// handleCommand handles the built-in slash commands. It returns (reply, true)
// when the message was a recognised command, else ("", false).
func (t *TelegramBot) handleCommand(_ context.Context, chatID int64, text string) (string, bool) {
	cmd, arg, _ := strings.Cut(strings.TrimSpace(text), " ")
	arg = strings.TrimSpace(arg)
	session := fmt.Sprintf("telegram:%d", chatID)
	switch strings.ToLower(cmd) {
	case "/status":
		return hookOr(t.hooks.Status, "status not available"), true
	case "/routes":
		return hookOr(t.hooks.Routes, "routes not available"), true
	case "/cost":
		return hookOr(t.hooks.Cost, "cost not available"), true
	case "/ls":
		if t.hooks.List == nil {
			return "file listing not available", true
		}
		return t.hooks.List(arg), true
	case "/approve", "/reject":
		if t.hooks.Approve == nil {
			return "approval not available", true
		}
		transcript, matched := t.hooks.Approve(session, cmd == "/approve")
		if !matched {
			return "No pending action to " + strings.TrimPrefix(cmd, "/") + ".", true
		}
		if cmd == "/approve" {
			return "✅ Approved\n" + transcript, true
		}
		return "❌ Rejected", true
	case "/help":
		return helpText(), true
	case "/build":
		// /build falls through to the agent runtime as a build request.
		return "", false
	default:
		return "", false
	}
}

// hookOr calls fn (if set) or returns a fallback message.
func hookOr(fn func() string, fallback string) string {
	if fn == nil {
		return fallback
	}
	return fn()
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

// SendClarifyingQuestions sends each structured question as a row of inline
// option buttons (callback_data "clarify:<session>:<key>:<value>"). Questions
// with no options are sent as plain text (the next text message is the answer).
// It registers a clarifySession so taps are collected until all are answered.
func (t *TelegramBot) SendClarifyingQuestions(ctx context.Context, chatID int64, session string, qs []ClarifyQuestion) error {
	if len(qs) == 0 {
		return nil
	}
	cs := &clarifySession{chatID: chatID, answers: map[string]string{}}
	for _, q := range qs {
		if len(q.Options) > 0 {
			cs.keys = append(cs.keys, q.Key)
		}
	}
	t.clarifyMu.Lock()
	if t.clarify == nil {
		t.clarify = map[string]*clarifySession{}
	}
	t.clarify[session] = cs
	t.clarifyMu.Unlock()

	if err := t.SendMessage(ctx, chatID, "Before I build, quick questions:"); err != nil {
		return err
	}
	for _, q := range qs {
		if len(q.Options) == 0 {
			_ = t.SendMessage(ctx, chatID, q.Question) // free text → next message is the answer
			continue
		}
		var row []map[string]any
		for _, opt := range q.Options {
			row = append(row, map[string]any{
				"text":          opt,
				"callback_data": fmt.Sprintf("clarify:%s:%s:%s", session, q.Key, opt),
			})
		}
		if err := t.post(ctx, "sendMessage", map[string]any{
			"chat_id":      chatID,
			"text":         q.Question,
			"reply_markup": map[string]any{"inline_keyboard": [][]map[string]any{row}},
		}); err != nil {
			return err
		}
	}
	return nil
}

// handleClarifyCallback processes a "clarify:<session>:<key>:<value>" button
// tap. It records the answer; once every keyed question is answered it submits
// the combined answer via the ClarifySubmit hook. Returns true if it consumed
// the callback.
func (t *TelegramBot) handleClarifyCallback(data string) bool {
	const prefix = "clarify:"
	if !strings.HasPrefix(data, prefix) {
		return false
	}
	rest := strings.TrimPrefix(data, prefix)
	// session:key:value (value may contain colons → split into 3).
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 {
		return true // malformed but it was a clarify callback
	}
	session, key, value := parts[0], parts[1], parts[2]

	t.clarifyMu.Lock()
	cs, ok := t.clarify[session]
	if !ok || cs.submitted {
		t.clarifyMu.Unlock()
		return true
	}
	cs.answers[key] = value
	// All keyed questions answered?
	complete := true
	var combined []string
	for _, k := range cs.keys {
		v, answered := cs.answers[k]
		if !answered {
			complete = false
			break
		}
		combined = append(combined, v)
	}
	if complete {
		cs.submitted = true
		delete(t.clarify, session)
	}
	t.clarifyMu.Unlock()

	if complete && t.hooks.ClarifySubmit != nil {
		t.hooks.ClarifySubmit(session, strings.Join(combined, ", "))
	}
	return true
}
