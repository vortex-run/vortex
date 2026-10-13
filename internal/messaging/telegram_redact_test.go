package messaging

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// errFailingTransport is returned by every request the fake transport sees, so
// these tests exercise the transport-error path without touching the network.
var errFailingTransport = errors.New("simulated dial failure")

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errFailingTransport
}

// The token used throughout. It must be a value that cannot occur incidentally
// in an error string, so a match proves it came from the credential.
const testToken = "1111111111:FAKE-TEST-TOKEN-NOT-A-REAL-CREDENTIAL"

func newLeakyBot(t *testing.T) *TelegramBot {
	t.Helper()
	// BaseURL is deliberately NOT set: the real code derives it as
	// "https://api.telegram.org/bot"+token, and that derivation is precisely
	// what puts the credential into every *url.Error. Overriding BaseURL here
	// would make the test pass against the unfixed code.
	bot, err := NewTelegramBot(TelegramConfig{
		Token:      testToken,
		AllowedIDs: []int64{42},
		Client:     &http.Client{Transport: failingTransport{}},
	})
	if err != nil {
		t.Fatalf("NewTelegramBot: %v", err)
	}
	return bot
}

// TestSendMessage_ErrorDoesNotLeakToken is the regression. Before the fix,
// post() returned the raw *url.Error from client.Do, whose message is
// `Post "https://api.telegram.org/bot<TOKEN>/sendMessage": ...` — so a routine
// network blip logged a live credential at WARN level.
func TestSendMessage_ErrorDoesNotLeakToken(t *testing.T) {
	bot := newLeakyBot(t)

	err := bot.SendMessage(context.Background(), 42, "hello")
	if err == nil {
		t.Fatal("expected the failing transport to produce an error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("bot token leaked into the error message: %s", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Errorf("error should show the token was redacted, got: %s", err)
	}
}

// TestGetUpdates_ErrorDoesNotLeakToken covers the polling path, which runs on a
// loop — so an outage there reproduces the leak on every iteration rather than
// once.
func TestGetUpdates_ErrorDoesNotLeakToken(t *testing.T) {
	bot := newLeakyBot(t)

	_, err := bot.getUpdates(context.Background(), 0)
	if err == nil {
		t.Fatal("expected the failing transport to produce an error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("bot token leaked into the polling error: %s", err)
	}
}

func TestSendFile_ErrorDoesNotLeakToken(t *testing.T) {
	bot := newLeakyBot(t)

	err := bot.SendFile(context.Background(), 42, "report.txt", []byte("data"), "caption")
	if err == nil {
		t.Fatal("expected the failing transport to produce an error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("bot token leaked into the upload error: %s", err)
	}
}

// TestRedact_PreservesErrorChain guards the fix against being implemented with
// a plain fmt.Errorf that throws the cause away. Callers legitimately test for
// context.DeadlineExceeded and similar to decide whether to retry, so redaction
// must not sever errors.Is.
func TestRedact_PreservesErrorChain(t *testing.T) {
	bot := newLeakyBot(t)

	err := bot.SendMessage(context.Background(), 42, "hello")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, errFailingTransport) {
		t.Errorf("redaction severed the error chain; errors.Is cannot reach the cause: %v", err)
	}
}

// TestRedact_EmptyTokenIsNotMangled covers the trap in the implementation:
// strings.ReplaceAll(s, "", "<redacted>") inserts the replacement between every
// character. NewTelegramBot rejects an empty token, so this exercises redact
// directly.
func TestRedact_EmptyTokenIsNotMangled(t *testing.T) {
	bot := &TelegramBot{cfg: TelegramConfig{Token: ""}}
	original := errors.New("connection refused")

	got := bot.redact(original)
	if got.Error() != "connection refused" {
		t.Errorf("empty token mangled the message: %q", got.Error())
	}
}

// TestRedact_UnrelatedErrorKeepsIdentity: when there is nothing to redact the
// original error must be returned unchanged, so equality checks elsewhere in
// the codebase keep working.
func TestRedact_UnrelatedErrorKeepsIdentity(t *testing.T) {
	bot := newLeakyBot(t)
	original := errors.New("something unrelated")

	if got := bot.redact(original); got != original {
		t.Errorf("redact replaced an error with nothing to hide: %v", got)
	}
}

func TestRedact_NilStaysNil(t *testing.T) {
	bot := newLeakyBot(t)
	if got := bot.redact(nil); got != nil {
		t.Errorf("redact(nil) = %v, want nil", got)
	}
}
