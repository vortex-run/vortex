package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// AIGateway is the minimal completion interface the ContextBridge needs to
// compress a conversation. messaging.AIGateway satisfies it; defining it here
// keeps gateway free of a messaging import (which would cycle).
type AIGateway interface {
	Complete(ctx context.Context, prompt, systemPrompt string) (string, error)
}

// ContextMessage is one message in a preserved conversation, with an estimated
// token count.
type ContextMessage struct {
	Role    string `json:"role"` // user|assistant|system
	Content string `json:"content"`
	Tokens  int    `json:"tokens"`
}

// ConversationContext is a session's preserved conversation plus its
// compressed summary and the provider that was active.
type ConversationContext struct {
	Messages    []ContextMessage `json:"messages"`
	Summary     string           `json:"summary"`
	SessionID   string           `json:"session_id"`
	TotalTokens int              `json:"total_tokens"`
	Provider    string           `json:"provider"`
}

// compressTokenThreshold is the total-token level above which BuildHandoff
// compresses instead of carrying the full transcript.
const compressTokenThreshold = 4000

// summarizerSystemPrompt instructs the model to produce a dense, handoff-ready
// summary that preserves the state a new provider needs to continue.
const summarizerSystemPrompt = `You are a conversation summarizer. Create a dense summary that preserves:
1. All decisions made
2. All files created or modified
3. Current task state (what is in progress)
4. Key technical context (languages, frameworks)
5. User preferences stated
Return a paragraph of max 500 words.`

// ContextBridge preserves conversation context across provider switches (the
// key innovation: no context loss when a key rotates). It stores conversations
// in memory keyed by session and, when handing off to a new provider, either
// transfers recent messages verbatim or compresses a long conversation into a
// summary the new provider receives as system context.
type ContextBridge struct {
	gateway AIGateway

	mu    sync.Mutex
	convs map[string]*ConversationContext
}

// NewContextBridge constructs a ContextBridge over an AI gateway.
func NewContextBridge(gateway AIGateway) *ContextBridge {
	return &ContextBridge{gateway: gateway, convs: map[string]*ConversationContext{}}
}

// Store saves the full conversation for a session (filling token estimates and
// the total).
func (b *ContextBridge) Store(sessionID string, messages []ContextMessage) {
	total := 0
	msgs := make([]ContextMessage, len(messages))
	for i, m := range messages {
		if m.Tokens == 0 {
			m.Tokens = EstimateTokens(m.Content)
		}
		total += m.Tokens
		msgs[i] = m
	}
	b.mu.Lock()
	b.convs[sessionID] = &ConversationContext{
		Messages: msgs, SessionID: sessionID, TotalTokens: total,
	}
	b.mu.Unlock()
}

// Get returns a session's stored conversation, or nil.
func (b *ContextBridge) Get(sessionID string) *ConversationContext {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.convs[sessionID]
}

// Compress summarises a stored conversation via the AI gateway, caching and
// returning the summary. It errors when the session is unknown.
func (b *ContextBridge) Compress(ctx context.Context, sessionID string) (string, error) {
	conv := b.Get(sessionID)
	if conv == nil {
		return "", fmt.Errorf("gateway: no stored conversation for session %s", sessionID)
	}
	var sb strings.Builder
	for _, m := range conv.Messages {
		sb.WriteString(m.Role + ": " + m.Content + "\n")
	}
	summary, err := b.gateway.Complete(ctx,
		"Summarize this conversation:\n"+sb.String(), summarizerSystemPrompt)
	if err != nil {
		return "", fmt.Errorf("gateway: compressing context: %w", err)
	}
	summary = strings.TrimSpace(summary)
	b.mu.Lock()
	if c := b.convs[sessionID]; c != nil {
		c.Summary = summary
	}
	b.mu.Unlock()
	return summary, nil
}

// BuildHandoff builds the message list for a new provider, preserving full
// context: under the token threshold it transfers the last 20 messages as-is;
// over the threshold it compresses to a summary and carries the last 10
// messages verbatim behind a system message. It errors on an unknown session.
func (b *ContextBridge) BuildHandoff(ctx context.Context, sessionID, newProvider string) ([]ContextMessage, error) {
	conv := b.Get(sessionID)
	if conv == nil {
		return nil, fmt.Errorf("gateway: no stored conversation for session %s", sessionID)
	}
	b.mu.Lock()
	if conv2 := b.convs[sessionID]; conv2 != nil {
		conv2.Provider = newProvider
	}
	b.mu.Unlock()

	if conv.TotalTokens > compressTokenThreshold {
		summary, err := b.Compress(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		out := []ContextMessage{{
			Role:    "system",
			Content: "Previous context: " + summary,
			Tokens:  EstimateTokens(summary),
		}}
		out = append(out, lastN(conv.Messages, 10)...)
		return out, nil
	}
	return lastN(conv.Messages, 20), nil
}

// NotifySwitch returns a user-visible message describing a provider switch.
// carried is the number of messages carried over (the caller, which performed
// the handoff, knows this count).
func (b *ContextBridge) NotifySwitch(oldSlot, newSlot *KeySlot, reason string, carried int) string {
	oldName, newName := "previous provider", "new provider"
	if oldSlot != nil {
		oldName = oldSlot.Provider
	}
	if newSlot != nil {
		newName = newSlot.Provider
	}
	return fmt.Sprintf("[system] Switched from %s to %s\nReason: %s\nContext preserved — %d messages carried over",
		oldName, newName, reason, carried)
}

// EstimateTokens returns a rough token estimate (~4 characters per token).
// Good enough for routing/compression decisions.
func EstimateTokens(text string) int {
	return len(text) / 4
}

// lastN returns the last n messages of msgs (or all when fewer).
func lastN(msgs []ContextMessage, n int) []ContextMessage {
	if len(msgs) <= n {
		out := make([]ContextMessage, len(msgs))
		copy(out, msgs)
		return out
	}
	out := make([]ContextMessage, n)
	copy(out, msgs[len(msgs)-n:])
	return out
}
