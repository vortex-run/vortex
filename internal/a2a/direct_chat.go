package a2a

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Chatter is implemented by agents that support direct conversation. The user
// talks to the agent (asks why it made a decision, gives a correction) without
// submitting a formal task. BaseAgent satisfies this via its AI gateway.
type Chatter interface {
	// Chat answers a user message using the agent's own system prompt and the
	// supplied context (its current task state), returning the reply.
	Chat(ctx context.Context, taskContext, userMessage string) (string, error)
}

// ChatMessage is one turn of a direct-chat conversation.
type ChatMessage struct {
	Role      string    `json:"role"` // user|agent
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// ChatSession is a user's direct-chat history with one agent.
type ChatSession struct {
	SessionID string        `json:"session_id"`
	AgentID   string        `json:"agent_id"`
	Messages  []ChatMessage `json:"messages"`
	CreatedAt time.Time     `json:"created_at"`
}

// DirectChat lets the user converse directly with one agent while it works.
type DirectChat struct {
	agentID string
	agent   Agent
	chatter Chatter
	bus     *MessageBus

	mu       sync.Mutex
	sessions map[string]*ChatSession
}

// NewDirectChat constructs a DirectChat for an agent. When the agent also
// implements Chatter, Send routes to it; otherwise Send returns an error.
func NewDirectChat(agent Agent, bus *MessageBus) *DirectChat {
	d := &DirectChat{
		agentID:  agent.Card().ID,
		agent:    agent,
		bus:      bus,
		sessions: map[string]*ChatSession{},
	}
	if c, ok := agent.(Chatter); ok {
		d.chatter = c
	}
	return d
}

// AgentID returns the agent this chat belongs to.
func (d *DirectChat) AgentID() string { return d.agentID }

// Send delivers a user message directly to the agent and returns its reply.
// This is a conversation, not a task: the agent answers using its own system
// prompt + current context. The exchange is recorded and published to the bus.
func (d *DirectChat) Send(ctx context.Context, sessionID, userMessage string) (string, error) {
	if d.chatter == nil {
		return "", fmt.Errorf("a2a: agent %s does not support direct chat", d.agentID)
	}
	d.appendMessage(sessionID, "user", userMessage)
	d.publish(sessionID, "user", d.agentID, userMessage)

	reply, err := d.chatter.Chat(ctx, d.contextFor(sessionID), userMessage)
	if err != nil {
		return "", fmt.Errorf("a2a: direct chat with %s: %w", d.agentID, err)
	}
	d.appendMessage(sessionID, "agent", reply)
	d.publish(sessionID, d.agentID, "user", reply)
	return reply, nil
}

// History returns a session's conversation (empty when none).
func (d *DirectChat) History(sessionID string) []ChatMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s := d.sessions[sessionID]; s != nil {
		out := make([]ChatMessage, len(s.Messages))
		copy(out, s.Messages)
		return out
	}
	return nil
}

// appendMessage records a turn, creating the session if needed.
func (d *DirectChat) appendMessage(sessionID, role, content string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.sessions[sessionID]
	if s == nil {
		s = &ChatSession{SessionID: sessionID, AgentID: d.agentID, CreatedAt: time.Now()}
		d.sessions[sessionID] = s
	}
	s.Messages = append(s.Messages, ChatMessage{Role: role, Content: content, Timestamp: time.Now()})
}

// contextFor renders the recent conversation as context for the next reply.
func (d *DirectChat) contextFor(sessionID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.sessions[sessionID]
	if s == nil || len(s.Messages) <= 1 {
		return ""
	}
	var b []byte
	// Include the prior turns (excluding the just-appended user message).
	for _, m := range s.Messages[:len(s.Messages)-1] {
		b = append(b, (m.Role + ": " + m.Content + "\n")...)
	}
	return string(b)
}

// publish records the exchange on the bus for the UI's comms feed.
func (d *DirectChat) publish(sessionID, from, to, content string) {
	if d.bus == nil {
		return
	}
	d.bus.Publish(BusMessage{
		From: from, To: to, Type: MsgDirectChat, Content: content, SessionID: sessionID,
	})
}
