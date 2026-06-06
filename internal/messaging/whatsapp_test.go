package messaging

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeWhatsApp is an httptest server standing in for the Graph API.
type fakeWhatsApp struct {
	srv      *httptest.Server
	mu       sync.Mutex
	lastPath string
	lastJSON map[string]any
	gotAuth  string
}

func newFakeWhatsApp(t *testing.T) *fakeWhatsApp {
	t.Helper()
	f := &fakeWhatsApp{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lastPath = r.URL.Path
		f.gotAuth = r.Header.Get("Authorization")
		if strings.HasSuffix(r.URL.Path, "/media") {
			_, _ = io.WriteString(w, `{"id":"media-123"}`)
			return
		}
		f.lastJSON = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&f.lastJSON)
		_, _ = io.WriteString(w, `{"messages":[{"id":"wamid.1"}]}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newTestWA(t *testing.T, f *fakeWhatsApp, cfg WhatsAppConfig) *WhatsAppBot {
	t.Helper()
	cfg.PhoneNumberID = "PNID"
	cfg.AccessToken = "tok"
	cfg.BaseURL = f.srv.URL
	cfg.Client = f.srv.Client()
	bot, err := NewWhatsAppBot(cfg)
	if err != nil {
		t.Fatalf("NewWhatsAppBot: %v", err)
	}
	return bot
}

func TestNewWhatsAppBot_RequiresCredentials(t *testing.T) {
	if _, err := NewWhatsAppBot(WhatsAppConfig{PhoneNumberID: "x"}); err == nil {
		t.Error("expected error without access token")
	}
	if _, err := NewWhatsAppBot(WhatsAppConfig{AccessToken: "x"}); err == nil {
		t.Error("expected error without phone number id")
	}
}

func TestWhatsApp_SendText(t *testing.T) {
	f := newFakeWhatsApp(t)
	bot := newTestWA(t, f, WhatsAppConfig{})
	if err := bot.SendText(context.Background(), "15551234567", "hello"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if !strings.HasSuffix(f.lastPath, "/messages") {
		t.Errorf("path = %q, want .../messages", f.lastPath)
	}
	if f.lastJSON["messaging_product"] != "whatsapp" || f.lastJSON["to"] != "15551234567" {
		t.Errorf("body = %v", f.lastJSON)
	}
	txt, _ := f.lastJSON["text"].(map[string]any)
	if txt["body"] != "hello" {
		t.Errorf("text.body = %v, want hello", txt["body"])
	}
	if f.gotAuth != "Bearer tok" {
		t.Errorf("auth = %q, want Bearer tok", f.gotAuth)
	}
}

func TestWhatsApp_SendImage(t *testing.T) {
	f := newFakeWhatsApp(t)
	bot := newTestWA(t, f, WhatsAppConfig{})
	if err := bot.SendImage(context.Background(), "15551234567", []byte("PNG"), "a chart"); err != nil {
		t.Fatalf("SendImage: %v", err)
	}
	// Final call is the /messages send referencing the uploaded media id.
	if !strings.HasSuffix(f.lastPath, "/messages") {
		t.Errorf("path = %q, want .../messages", f.lastPath)
	}
	img, _ := f.lastJSON["image"].(map[string]any)
	if img["id"] != "media-123" || img["caption"] != "a chart" {
		t.Errorf("image = %v, want id=media-123 caption='a chart'", img)
	}
}

func TestWhatsApp_WebhookVerificationOK(t *testing.T) {
	f := newFakeWhatsApp(t)
	bot := newTestWA(t, f, WhatsAppConfig{VerifyToken: "verifyme"})
	rt := startStubRuntime(t)

	req := httptest.NewRequest(http.MethodGet,
		"/webhook/whatsapp?hub.mode=subscribe&hub.verify_token=verifyme&hub.challenge=CHAL", nil)
	rec := httptest.NewRecorder()
	bot.HandleWebhook(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "CHAL" {
		t.Errorf("verification: code=%d body=%q, want 200 CHAL", rec.Code, rec.Body.String())
	}
}

func TestWhatsApp_WebhookVerificationWrongToken(t *testing.T) {
	f := newFakeWhatsApp(t)
	bot := newTestWA(t, f, WhatsAppConfig{VerifyToken: "verifyme"})
	rt := startStubRuntime(t)

	req := httptest.NewRequest(http.MethodGet,
		"/webhook/whatsapp?hub.mode=subscribe&hub.verify_token=WRONG&hub.challenge=CHAL", nil)
	rec := httptest.NewRecorder()
	bot.HandleWebhook(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("wrong verify token code = %d, want 403", rec.Code)
	}
}

func signWA(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestWhatsApp_WebhookRoutesMessage(t *testing.T) {
	f := newFakeWhatsApp(t)
	bot := newTestWA(t, f, WhatsAppConfig{AppSecret: "appsecret"})
	rt := startStubRuntime(t)

	body := `{"entry":[{"changes":[{"value":{"messages":[{"from":"15551234567","text":{"body":"hello"}}]}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/whatsapp", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signWA("appsecret", body))
	rec := httptest.NewRecorder()
	bot.HandleWebhook(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Bot should have replied via /messages.
	if !strings.HasSuffix(f.lastPath, "/messages") {
		t.Errorf("expected a /messages reply, last path = %q", f.lastPath)
	}
}

func TestWhatsApp_WebhookRejectsBadSignature(t *testing.T) {
	f := newFakeWhatsApp(t)
	bot := newTestWA(t, f, WhatsAppConfig{AppSecret: "appsecret"})
	rt := startStubRuntime(t)

	body := `{"entry":[]}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/whatsapp", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signWA("WRONGSECRET", body))
	rec := httptest.NewRecorder()
	bot.HandleWebhook(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad signature code = %d, want 401", rec.Code)
	}
}

func TestWhatsApp_SendApprovalRequest(t *testing.T) {
	f := newFakeWhatsApp(t)
	bot := newTestWA(t, f, WhatsAppConfig{})
	if err := bot.SendApprovalRequest(context.Background(), "15551234567", "run build?"); err != nil {
		t.Fatalf("SendApprovalRequest: %v", err)
	}
	if f.lastJSON["type"] != "interactive" {
		t.Errorf("type = %v, want interactive", f.lastJSON["type"])
	}
}
