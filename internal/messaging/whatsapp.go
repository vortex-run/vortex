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
	"time"

	"github.com/vortex-run/vortex/internal/agents"
)

// WhatsAppConfig configures the WhatsApp Business (Meta Cloud API) bot. The
// phone-number ID and access token come from the environment, never config.
type WhatsAppConfig struct {
	PhoneNumberID string // from env VORTEX_WA_PHONE_ID
	AccessToken   string // from env VORTEX_WA_TOKEN
	VerifyToken   string // webhook GET verification (hub.verify_token)
	AppSecret     string // for X-Hub-Signature-256 HMAC verification
	BaseURL       string // override Graph API base (tests)
	Client        *http.Client
}

// WhatsAppBot is a WhatsApp Cloud API client + webhook handler.
type WhatsAppBot struct {
	cfg     WhatsAppConfig
	baseURL string
	client  *http.Client
}

// NewWhatsAppBot constructs the bot. It requires PhoneNumberID and AccessToken.
func NewWhatsAppBot(cfg WhatsAppConfig) (*WhatsAppBot, error) {
	if cfg.PhoneNumberID == "" || cfg.AccessToken == "" {
		return nil, fmt.Errorf("messaging: whatsapp requires phone number id and access token")
	}
	base := cfg.BaseURL
	if base == "" {
		base = "https://graph.facebook.com/v18.0/" + cfg.PhoneNumberID
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &WhatsAppBot{cfg: cfg, baseURL: base, client: client}, nil
}

// post issues an authenticated POST with a JSON body to the given path under
// the phone-number base URL.
func (wb *WhatsAppBot) post(ctx context.Context, path string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wb.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+wb.cfg.AccessToken)
	resp, err := wb.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("messaging: whatsapp %s: status %d: %s", path, resp.StatusCode, data)
	}
	return data, nil
}

// SendText sends a plain text message to the recipient phone number.
func (wb *WhatsAppBot) SendText(ctx context.Context, to, text string) error {
	_, err := wb.post(ctx, "/messages", map[string]any{
		"messaging_product": "whatsapp",
		"to":                to,
		"type":              "text",
		"text":              map[string]any{"body": text},
	})
	return err
}

// SendImage uploads imageData and sends it as an image message with a caption.
func (wb *WhatsAppBot) SendImage(ctx context.Context, to string, imageData []byte, caption string) error {
	mediaID, err := wb.uploadMedia(ctx, imageData)
	if err != nil {
		return err
	}
	_, err = wb.post(ctx, "/messages", map[string]any{
		"messaging_product": "whatsapp",
		"to":                to,
		"type":              "image",
		"image":             map[string]any{"id": mediaID, "caption": caption},
	})
	return err
}

// uploadMedia uploads bytes to the /media endpoint and returns the media ID.
func (wb *WhatsAppBot) uploadMedia(ctx context.Context, data []byte) (string, error) {
	// The Cloud API media upload is multipart; for our purposes (and to keep
	// the fake server simple) we post the bytes and read back the returned id.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wb.baseURL+"/media", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+wb.cfg.AccessToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := wb.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("messaging: whatsapp media upload: status %d: %s", resp.StatusCode, body)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// verifySignature validates the X-Hub-Signature-256 header (HMAC-SHA256 of the
// raw body keyed by the app secret). When no app secret is configured it
// returns true (tests/back-compat); production wiring always sets one.
func (wb *WhatsAppBot) verifySignature(body []byte, header string) bool {
	if wb.cfg.AppSecret == "" {
		return true
	}
	const prefix = "sha256="
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	mac := hmac.New(sha256.New, []byte(wb.cfg.AppSecret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	got := header[len(prefix):]
	return hmac.Equal([]byte(want), []byte(got))
}

// whatsappInbound is the subset of the webhook POST payload we consume.
type whatsappInbound struct {
	Entry []struct {
		Changes []struct {
			Value struct {
				Messages []struct {
					From string `json:"from"`
					Text struct {
						Body string `json:"body"`
					} `json:"text"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// HandleWebhook returns the http.Handler for the WhatsApp webhook. GET performs
// Meta's verification handshake; POST verifies the HMAC signature, extracts
// inbound messages, submits them to the runtime, and replies.
func (wb *WhatsAppBot) HandleWebhook(runtime *agents.Runtime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			wb.handleVerification(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		if !wb.verifySignature(body, r.Header.Get("X-Hub-Signature-256")) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		var in whatsappInbound
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		for _, e := range in.Entry {
			for _, c := range e.Changes {
				for _, m := range c.Value.Messages {
					wb.dispatch(r.Context(), runtime, m.From, m.Text.Body)
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

// handleVerification answers Meta's GET verification challenge.
func (wb *WhatsAppBot) handleVerification(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("hub.mode") == "subscribe" && q.Get("hub.verify_token") == wb.cfg.VerifyToken {
		_, _ = io.WriteString(w, q.Get("hub.challenge"))
		return
	}
	http.Error(w, "verification failed", http.StatusForbidden)
}

// dispatch submits text to the runtime and replies to the sender.
func (wb *WhatsAppBot) dispatch(ctx context.Context, runtime *agents.Runtime, from, text string) {
	if runtime == nil || text == "" || from == "" {
		return
	}
	ch, err := runtime.Submit(ctx, text, "whatsapp:"+from)
	if err != nil {
		_ = wb.SendText(ctx, from, "⚠️ "+err.Error())
		return
	}
	var resp string
	for chunk := range ch {
		resp += chunk
	}
	if resp != "" {
		_ = wb.SendText(ctx, from, resp)
	}
}

// SendApprovalRequest sends an interactive button message (Approve / Reject)
// used by the human-in-the-loop flow.
func (wb *WhatsAppBot) SendApprovalRequest(ctx context.Context, to, description string) error {
	_, err := wb.post(ctx, "/messages", map[string]any{
		"messaging_product": "whatsapp",
		"to":                to,
		"type":              "interactive",
		"interactive": map[string]any{
			"type": "button",
			"body": map[string]any{"text": description},
			"action": map[string]any{
				"buttons": []map[string]any{
					{"type": "reply", "reply": map[string]any{"id": "approve", "title": "Approve"}},
					{"type": "reply", "reply": map[string]any{"id": "reject", "title": "Reject"}},
				},
			},
		},
	})
	return err
}
