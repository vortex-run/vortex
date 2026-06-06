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
