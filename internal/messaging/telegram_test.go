package messaging

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vortex-run/vortex/internal/agents"
)

// fakeTelegram is an httptest server standing in for the Telegram Bot API. It
// records the last method called and its decoded JSON body (or multipart form).
type fakeTelegram struct {
	srv        *httptest.Server
	mu         sync.Mutex
	lastMethod string
	lastJSON   map[string]any
	lastMulti  map[string]string
	lastFile   []byte
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		// Method is the last path segment, e.g. /sendMessage.
		f.lastMethod = strings.TrimPrefix(r.URL.Path, "/")

		ct := r.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "multipart/form-data") {
			_, params, _ := mime.ParseMediaType(ct)
			mr := multipart.NewReader(r.Body, params["boundary"])
			f.lastMulti = map[string]string{}
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				data, _ := io.ReadAll(p)
				if p.FileName() != "" {
					f.lastFile = data
				} else {
					f.lastMulti[p.FormName()] = string(data)
				}
			}
		} else {
			f.lastJSON = map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&f.lastJSON)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTelegram) method() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastMethod
}

func newTestBot(t *testing.T, f *fakeTelegram, cfg TelegramConfig) *TelegramBot {
	t.Helper()
	cfg.Token = "test-token"
	cfg.BaseURL = f.srv.URL
	cfg.Client = f.srv.Client()
	bot, err := NewTelegramBot(cfg)
	if err != nil {
		t.Fatalf("NewTelegramBot: %v", err)
	}
	return bot
}

func TestNewTelegramBot_RequiresToken(t *testing.T) {
	if _, err := NewTelegramBot(TelegramConfig{}); err == nil {
		t.Error("expected error with empty token")
	}
}

func TestTelegram_SendMessage(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{})
	if err := bot.SendMessage(context.Background(), 42, "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if f.method() != "sendMessage" {
		t.Errorf("method = %q, want sendMessage", f.method())
	}
	if f.lastJSON["text"] != "hello" {
		t.Errorf("text = %v, want hello", f.lastJSON["text"])
	}
	if f.lastJSON["parse_mode"] != "Markdown" {
		t.Errorf("parse_mode = %v, want Markdown", f.lastJSON["parse_mode"])
	}
	if f.lastJSON["chat_id"] != float64(42) {
		t.Errorf("chat_id = %v, want 42", f.lastJSON["chat_id"])
	}
}

func TestTelegram_SendFile(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{})
	if err := bot.SendFile(context.Background(), 7, "app.apk", []byte("APKDATA"), "build done"); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if f.method() != "sendDocument" {
		t.Errorf("method = %q, want sendDocument", f.method())
	}
	if f.lastMulti["chat_id"] != "7" || f.lastMulti["caption"] != "build done" {
		t.Errorf("multipart fields = %v", f.lastMulti)
	}
	if string(f.lastFile) != "APKDATA" {
		t.Errorf("uploaded file = %q, want APKDATA", f.lastFile)
	}
}

func TestTelegram_SetWebhook(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{SecretToken: "s3cr3t"})
	if err := bot.SetWebhook(context.Background(), "https://x.example/webhook"); err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	if f.method() != "setWebhook" {
		t.Errorf("method = %q, want setWebhook", f.method())
	}
	if f.lastJSON["url"] != "https://x.example/webhook" {
		t.Errorf("url = %v", f.lastJSON["url"])
	}
	if f.lastJSON["secret_token"] != "s3cr3t" {
		t.Errorf("secret_token = %v, want s3cr3t", f.lastJSON["secret_token"])
	}
}

func TestTelegram_SendApprovalRequest(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{})
	if err := bot.SendApprovalRequest(context.Background(), 1, "run go build?", "approve:1", "reject:1"); err != nil {
		t.Fatalf("SendApprovalRequest: %v", err)
	}
	rm, ok := f.lastJSON["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("reply_markup missing or wrong type: %v", f.lastJSON["reply_markup"])
	}
	if _, ok := rm["inline_keyboard"]; !ok {
		t.Errorf("inline_keyboard missing: %v", rm)
	}
}

// startStubRuntime builds a started agent runtime backed by the stub gateway.
func startStubRuntime(t *testing.T) *agents.Runtime {
	t.Helper()
	bus := agents.NewBus()
	c, err := agents.NewCoordinator(agents.CoordinatorConfig{
		Bus:       bus,
		AIGateway: agents.StubAIGateway{IntentReply: string(agents.IntentGeneralQuestion), AnswerReply: "stub reply"},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	rt, err := agents.NewRuntime(agents.RuntimeConfig{Bus: bus, Coordinator: c, SandboxBase: t.TempDir()})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })
	return rt
}

func TestTelegram_HandleWebhook_RoutesToRuntime(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{AllowedIDs: []int64{99}})
	rt := startStubRuntime(t)

	body := `{"message":{"chat":{"id":99},"text":"hi there"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/telegram", strings.NewReader(body))
	rec := httptest.NewRecorder()
	bot.HandleWebhook(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// The bot should have replied via sendMessage to the fake API.
	if f.method() != "sendMessage" {
		t.Errorf("expected a sendMessage reply, last method = %q", f.method())
	}
}

func TestTelegram_HandleWebhook_RejectsUnknownChat(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{AllowedIDs: []int64{1}})
	rt := startStubRuntime(t)

	body := `{"message":{"chat":{"id":999},"text":"hi"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/telegram", strings.NewReader(body))
	rec := httptest.NewRecorder()
	bot.HandleWebhook(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("unknown chat status = %d, want 403", rec.Code)
	}
}

func TestTelegram_HandleWebhook_RejectsBadSecret(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{AllowedIDs: []int64{1}, SecretToken: "good"})
	rt := startStubRuntime(t)

	req := httptest.NewRequest(http.MethodPost, "/webhook/telegram",
		strings.NewReader(`{"message":{"chat":{"id":1},"text":"hi"}}`))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong")
	rec := httptest.NewRecorder()
	bot.HandleWebhook(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad secret status = %d, want 401", rec.Code)
	}
}

func TestTelegram_HandleWebhook_AcceptsGoodSecret(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{AllowedIDs: []int64{1}, SecretToken: "good"})
	rt := startStubRuntime(t)

	req := httptest.NewRequest(http.MethodPost, "/webhook/telegram",
		strings.NewReader(`{"message":{"chat":{"id":1},"text":"hi"}}`))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "good")
	rec := httptest.NewRecorder()
	bot.HandleWebhook(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("good secret status = %d, want 200", rec.Code)
	}
}

func TestTelegram_HandleWebhook_CallbackQuery(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{AllowedIDs: []int64{55}})
	rt := startStubRuntime(t)

	body := `{"callback_query":{"id":"cb1","data":"approve:job","from":{"id":55},"message":{"chat":{"id":55}}}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/telegram", strings.NewReader(body))
	rec := httptest.NewRecorder()
	bot.HandleWebhook(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("callback status = %d, want 200", rec.Code)
	}
}

func TestTelegram_SetCommands(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{})
	if err := bot.SetCommands(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.method() != "setMyCommands" {
		t.Errorf("method = %q, want setMyCommands", f.method())
	}
}

func TestTelegram_HandleCommandHooks(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{AllowedIDs: []int64{7}})
	bot.SetCommandHooks(CommandHooks{
		Status: func() string { return "VORTEX ok" },
		Cost:   func() string { return "free" },
		List:   func(string) string { return "files: a b" },
		Approve: func(_ string, ok bool) (string, bool) {
			if ok {
				return "✓ File created", true
			}
			return "", true
		},
	})
	cases := map[string]string{
		"/status":  "VORTEX ok",
		"/cost":    "free",
		"/ls .":    "files: a b",
		"/help":    "VORTEX commands",
		"/approve": "Approved",
	}
	for cmd, want := range cases {
		reply, handled := bot.handleCommand(context.Background(), 7, cmd)
		if !handled {
			t.Errorf("%s should be handled", cmd)
		}
		if !strings.Contains(reply, want) {
			t.Errorf("%s reply = %q, want it to contain %q", cmd, reply, want)
		}
	}
	// A non-command falls through.
	if _, handled := bot.handleCommand(context.Background(), 7, "hello there"); handled {
		t.Error("a non-command should not be handled")
	}
}

func TestTelegram_RejectCommand(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{})
	bot.SetCommandHooks(CommandHooks{
		Approve: func(string, bool) (string, bool) { return "", true },
	})
	reply, handled := bot.handleCommand(context.Background(), 1, "/reject")
	if !handled || !strings.Contains(reply, "Rejected") {
		t.Errorf("/reject = %q handled=%v", reply, handled)
	}
}

func TestTelegram_GetUpdates(t *testing.T) {
	// A fake that returns one message update for getUpdates.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getUpdates") {
			_, _ = io.WriteString(w, `{"ok":true,"result":[{"update_id":5,"message":{"chat":{"id":7},"text":"hi"}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	bot, _ := NewTelegramBot(TelegramConfig{Token: "t", BaseURL: srv.URL, Client: srv.Client()})
	updates, err := bot.getUpdates(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].UpdateID != 5 || updates[0].Message.Text != "hi" {
		t.Errorf("getUpdates = %+v", updates)
	}
	if bot.lastUpdateID(updates[0]) != 5 {
		t.Errorf("lastUpdateID = %d, want 5", bot.lastUpdateID(updates[0]))
	}
}

func TestTelegram_HelpText(t *testing.T) {
	h := helpText()
	for _, c := range []string{"/status", "/ls", "/approve", "/cost", "/help"} {
		if !strings.Contains(h, c) {
			t.Errorf("help text missing %s", c)
		}
	}
}

func TestTelegram_SendClarifyingQuestions(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{AllowedIDs: []int64{7}})
	qs := []ClarifyQuestion{
		{Question: "Calculator type?", Key: "calc", Options: []string{"Basic", "Scientific"}},
		{Question: "Platform?", Key: "plat", Options: []string{"Phone", "Tablet"}},
	}
	if err := bot.SendClarifyingQuestions(context.Background(), 7, "sess-1", qs); err != nil {
		t.Fatal(err)
	}
	// A clarify session is registered with both keys.
	bot.clarifyMu.Lock()
	cs := bot.clarify["sess-1"]
	bot.clarifyMu.Unlock()
	if cs == nil || len(cs.keys) != 2 {
		t.Fatalf("clarify session not registered: %+v", cs)
	}
}

func TestTelegram_ClarifyCallbackCollectsAndSubmits(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{AllowedIDs: []int64{7}})
	var submitted string
	bot.SetCommandHooks(CommandHooks{
		ClarifySubmit: func(_ string, answer string) { submitted = answer },
	})
	qs := []ClarifyQuestion{
		{Question: "Calculator type?", Key: "calc", Options: []string{"Basic", "Scientific"}},
		{Question: "Platform?", Key: "plat", Options: []string{"Phone", "Tablet"}},
	}
	_ = bot.SendClarifyingQuestions(context.Background(), 7, "sess-2", qs)

	// First tap: not yet complete.
	if !bot.handleClarifyCallback("clarify:sess-2:calc:Scientific") {
		t.Error("first clarify tap should be consumed")
	}
	if submitted != "" {
		t.Error("should not submit until all questions answered")
	}
	// Second tap: complete → submit combined answer in question order.
	bot.handleClarifyCallback("clarify:sess-2:plat:Phone")
	if submitted != "Scientific, Phone" {
		t.Errorf("combined answer = %q, want 'Scientific, Phone'", submitted)
	}
}

func TestTelegram_ClarifyCallbackUnknownSession(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{})
	// A clarify callback for an unknown session is consumed but does nothing.
	if !bot.handleClarifyCallback("clarify:ghost:k:v") {
		t.Error("clarify callback should be consumed even for unknown session")
	}
}

func TestTelegram_NonClarifyCallbackNotConsumed(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{})
	if bot.handleClarifyCallback("approve:appr-1") {
		t.Error("a non-clarify callback should not be consumed by the clarify handler")
	}
}
