package messaging

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// WhatsApp signature verification accepts exactly the correct HMAC header.
func FuzzWhatsAppVerifySignature(f *testing.F) {
	f.Add([]byte(`{"entry":[]}`), "sha256=00")
	f.Add([]byte(""), "")
	f.Add([]byte("x"), "sha256=")
	bot := &WhatsAppBot{cfg: WhatsAppConfig{AppSecret: "appsecret"}}
	f.Fuzz(func(t *testing.T, body []byte, header string) {
		mac := hmac.New(sha256.New, []byte("appsecret"))
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if got := bot.verifySignature(body, header); got != (header == want) {
			t.Fatalf("verifySignature(header=%q) = %v, want %v", header, got, header == want)
		}
		// The correct signature always passes.
		if !bot.verifySignature(body, want) {
			t.Fatal("correct signature rejected")
		}
	})
}

// Slack verification accepts exactly the correct signature for a fresh
// timestamp, and never a stale one.
func FuzzSlackVerifySignature(f *testing.F) {
	now := time.Unix(1_800_000_000, 0)
	f.Add([]byte("text=status"), int64(0), "v0=00")
	f.Add([]byte(""), int64(301), "")
	f.Add([]byte("a=b"), int64(-301), "v0=")
	bot := &SlackBot{cfg: SlackConfig{SigningSecret: "shh"}, now: func() time.Time { return now }}
	f.Fuzz(func(t *testing.T, body []byte, skew int64, sig string) {
		if skew > 1<<40 || skew < -(1<<40) {
			return
		}
		ts := strconv.FormatInt(now.Unix()+skew, 10)
		mac := hmac.New(sha256.New, []byte("shh"))
		fmt.Fprintf(mac, "v0:%s:%s", ts, body)
		want := "v0=" + hex.EncodeToString(mac.Sum(nil))
		fresh := skew <= 300 && skew >= -300
		if got := slackVerify(bot, body, ts, sig); got != (fresh && sig == want) {
			t.Fatalf("verifySignature(skew=%d, sig=%q) = %v, want %v", skew, sig, got, fresh && sig == want)
		}
		if got := slackVerify(bot, body, ts, want); got != fresh {
			t.Fatalf("correct signature with skew %d: got %v, want %v", skew, got, fresh)
		}
	})
}

func slackVerify(b *SlackBot, body []byte, ts, sig string) bool {
	return b.verifySignature(body, ts, sig)
}

// NormalizeWhatsAppNumber keeps only ASCII digits and is idempotent.
func FuzzNormalizeWhatsAppNumber(f *testing.F) {
	for _, seed := range []string{"+1 (555) 010-0000", "١٢٣", "abc", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := NormalizeWhatsAppNumber(in)
		if strings.Trim(out, "0123456789") != "" {
			t.Fatalf("NormalizeWhatsAppNumber(%q) = %q contains non-digits", in, out)
		}
		if NormalizeWhatsAppNumber(out) != out {
			t.Fatalf("not idempotent for %q", in)
		}
	})
}
