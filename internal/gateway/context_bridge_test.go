package gateway

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// mockGateway is a fake AIGateway that returns a fixed summary and records the
// prompt it received.
type mockGateway struct {
	summary    string
	err        error
	lastPrompt string
	lastSystem string
	calls      int
}

func (g *mockGateway) Complete(_ context.Context, prompt, system string) (string, error) {
	g.calls++
	g.lastPrompt = prompt
	g.lastSystem = system
	if g.err != nil {
		return "", g.err
	}
	return g.summary, nil
}

func msgs(n int) []ContextMessage {
	out := make([]ContextMessage, n)
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		out[i] = ContextMessage{Role: role, Content: fmt.Sprintf("message number %d", i)}
	}
	return out
}

// bigMsgs returns messages whose total token estimate exceeds the threshold.
func bigMsgs(n int) []ContextMessage {
	body := strings.Repeat("x", 400) // ~100 tokens each
	out := make([]ContextMessage, n)
	for i := 0; i < n; i++ {
		out[i] = ContextMessage{Role: "user", Content: fmt.Sprintf("%d %s", i, body)}
	}
	return out
}

func TestBridge_StoreByteSession(t *testing.T) {
	b := NewContextBridge(&mockGateway{})
	b.Store("s1", msgs(3))
	conv := b.Get("s1")
	if conv == nil || len(conv.Messages) != 3 {
		t.Fatalf("Store/Get round-trip failed: %+v", conv)
	}
	// Token estimates are filled in.
	if conv.Messages[0].Tokens == 0 || conv.TotalTokens == 0 {
		t.Errorf("token estimates not populated: %+v", conv)
	}
}

func TestBridge_MultipleSessionsIsolated(t *testing.T) {
	b := NewContextBridge(&mockGateway{})
	b.Store("s1", msgs(2))
	b.Store("s2", msgs(5))
	if len(b.Get("s1").Messages) != 2 || len(b.Get("s2").Messages) != 5 {
		t.Error("sessions interfere with each other")
	}
	if b.Get("unknown") != nil {
		t.Error("unknown session should be nil")
	}
}

func TestBridge_Compress(t *testing.T) {
	gw := &mockGateway{summary: "Dense summary of the work."}
	b := NewContextBridge(gw)
	b.Store("s1", msgs(4))
	got, err := b.Compress(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if got != "Dense summary of the work." {
		t.Errorf("summary = %q", got)
	}
	if !strings.Contains(gw.lastSystem, "summarizer") {
		t.Errorf("summarizer system prompt not used: %q", gw.lastSystem)
	}
	if !strings.Contains(gw.lastPrompt, "message number 0") {
		t.Errorf("conversation not passed to summarizer: %q", gw.lastPrompt)
	}
	// The summary is cached on the conversation.
	if b.Get("s1").Summary != got {
		t.Error("summary not cached")
	}
}

func TestBridge_CompressUnknownSession(t *testing.T) {
	b := NewContextBridge(&mockGateway{})
	if _, err := b.Compress(context.Background(), "nope"); err == nil {
		t.Error("Compress of unknown session should error")
	}
}

func TestBridge_CompressAIError(t *testing.T) {
	b := NewContextBridge(&mockGateway{err: fmt.Errorf("provider down")})
	b.Store("s1", msgs(3))
	if _, err := b.Compress(context.Background(), "s1"); err == nil {
		t.Error("AI error should propagate")
	}
}

func TestBridge_HandoffUnderThresholdTransfersVerbatim(t *testing.T) {
	gw := &mockGateway{summary: "should not be used"}
	b := NewContextBridge(gw)
	b.Store("s1", msgs(25)) // small messages, well under 4000 tokens
	out, err := b.BuildHandoff(context.Background(), "s1", "claude")
	if err != nil {
		t.Fatalf("BuildHandoff: %v", err)
	}
	// Last 20 messages verbatim, no compression call.
	if len(out) != 20 {
		t.Errorf("handoff = %d messages, want 20", len(out))
	}
	if gw.calls != 0 {
		t.Error("under-threshold handoff should NOT call the AI")
	}
	if out[0].Role == "system" && strings.Contains(out[0].Content, "Previous context") {
		t.Error("under-threshold handoff should not inject a summary")
	}
}

func TestBridge_HandoffOverThresholdCompresses(t *testing.T) {
	gw := &mockGateway{summary: "Compressed state: building a FastAPI app."}
	b := NewContextBridge(gw)
	b.Store("s1", bigMsgs(60)) // ~6000 tokens, over the 4000 threshold
	out, err := b.BuildHandoff(context.Background(), "s1", "claude")
	if err != nil {
		t.Fatalf("BuildHandoff: %v", err)
	}
	if gw.calls != 1 {
		t.Fatalf("over-threshold handoff should compress once, got %d calls", gw.calls)
	}
	// First message is the summary system message, then the last 10 verbatim.
	if out[0].Role != "system" || !strings.Contains(out[0].Content, "Previous context") {
		t.Errorf("first handoff message should carry the summary: %+v", out[0])
	}
	if !strings.Contains(out[0].Content, "FastAPI") {
		t.Errorf("summary content missing: %q", out[0].Content)
	}
	if len(out) != 11 { // 1 summary + 10 recent
		t.Errorf("handoff = %d messages, want 11 (summary + 10)", len(out))
	}
}

func TestBridge_HandoffUnknownSession(t *testing.T) {
	b := NewContextBridge(&mockGateway{})
	if _, err := b.BuildHandoff(context.Background(), "nope", "claude"); err == nil {
		t.Error("handoff of unknown session should error")
	}
}

func TestBridge_HandoffRecordsProvider(t *testing.T) {
	b := NewContextBridge(&mockGateway{})
	b.Store("s1", msgs(3))
	_, _ = b.BuildHandoff(context.Background(), "s1", "groq")
	if b.Get("s1").Provider != "groq" {
		t.Errorf("provider = %q, want groq", b.Get("s1").Provider)
	}
}

func TestBridge_NotifySwitch(t *testing.T) {
	b := NewContextBridge(&mockGateway{})
	old := &KeySlot{Provider: "deepseek"}
	nw := &KeySlot{Provider: "groq"}
	msg := b.NotifySwitch(old, nw, "rate limited", 7)
	for _, want := range []string{"deepseek", "groq", "rate limited", "7 messages"} {
		if !strings.Contains(msg, want) {
			t.Errorf("NotifySwitch missing %q:\n%s", want, msg)
		}
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens("abcd"); got != 1 {
		t.Errorf("EstimateTokens(4 chars) = %d, want 1", got)
	}
	if got := EstimateTokens(strings.Repeat("x", 400)); got != 100 {
		t.Errorf("EstimateTokens(400 chars) = %d, want 100", got)
	}
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(empty) = %d, want 0", got)
	}
}
