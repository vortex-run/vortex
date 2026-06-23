package a2a

import (
	"sync"
	"time"
)

// BusMessage is one observable message on the agent communication bus: a task
// assignment, a result, a progress line, a user message, or an agent reply.
// The TUI and dashboard subscribe to render the full inter-agent conversation
// in real time.
type BusMessage struct {
	ID        string         `json:"id"`
	From      string         `json:"from"`    // agent ID (or "user")
	To        string         `json:"to"`      // agent ID or "user"
	Type      string         `json:"type"`    // task|result|progress|user-msg|agent-msg|checkpoint|direct-chat
	Content   string         `json:"content"` // the message text
	Metadata  map[string]any `json:"metadata,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	SessionID string         `json:"session_id"`
}

// Bus message types.
const (
	MsgTask       = "task"
	MsgResult     = "result"
	MsgProgress   = "progress"
	MsgUser       = "user-msg"
	MsgAgent      = "agent-msg"
	MsgCheckpoint = "checkpoint"
	MsgDirectChat = "direct-chat"
	MsgToolResult = "tool_result"
	MsgPlan       = "plan"
)

// busHistoryCap bounds the append-only history so a long session cannot grow
// the bus without limit; the oldest messages are evicted.
const busHistoryCap = 5000

// busSubBuffer is each subscriber channel's buffer depth.
const busSubBuffer = 100

// MessageBus is an append-only, fan-out bus for agent communication. Publish
// never blocks: a slow subscriber drops messages rather than stalling the
// agents. Safe for concurrent use.
type MessageBus struct {
	mu        sync.RWMutex
	messages  []BusMessage
	listeners map[int]chan BusMessage
	nextSub   int
}

// NewMessageBus constructs an empty bus.
func NewMessageBus() *MessageBus {
	return &MessageBus{listeners: map[int]chan BusMessage{}}
}

// Publish appends a message to history and fans it out to all subscribers. A
// missing ID/timestamp is filled in. It never blocks.
func (b *MessageBus) Publish(msg BusMessage) {
	if msg.ID == "" {
		msg.ID = "msg-" + randomID()
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	b.mu.Lock()
	b.messages = append(b.messages, msg)
	if len(b.messages) > busHistoryCap {
		b.messages = b.messages[len(b.messages)-busHistoryCap:]
	}
	subs := make([]chan BusMessage, 0, len(b.listeners))
	for _, ch := range b.listeners {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- msg:
		default: // subscriber buffer full; drop rather than stall the publisher
		}
	}
}

// Subscribe returns a channel of future messages plus an unsubscribe func that
// closes the channel. Buffer is 100.
func (b *MessageBus) Subscribe() (<-chan BusMessage, func()) {
	ch := make(chan BusMessage, busSubBuffer)
	b.mu.Lock()
	id := b.nextSub
	b.nextSub++
	b.listeners[id] = ch
	b.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			if c, ok := b.listeners[id]; ok {
				delete(b.listeners, id)
				close(c)
			}
			b.mu.Unlock()
		})
	}
	return ch, unsub
}

// History returns up to limit recent messages, filtered by sessionID when
// non-empty. limit <= 0 returns all matching messages.
func (b *MessageBus) History(sessionID string, limit int) []BusMessage {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []BusMessage
	for _, m := range b.messages {
		if sessionID != "" && m.SessionID != sessionID {
			continue
		}
		out = append(out, m)
	}
	return tailN(out, limit)
}

// AgentMessages returns up to limit recent messages to or from agentID.
func (b *MessageBus) AgentMessages(agentID string, limit int) []BusMessage {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []BusMessage
	for _, m := range b.messages {
		if m.From == agentID || m.To == agentID {
			out = append(out, m)
		}
	}
	return tailN(out, limit)
}

// tailN returns the last n elements of msgs (all when n <= 0).
func tailN(msgs []BusMessage, n int) []BusMessage {
	if n <= 0 || len(msgs) <= n {
		return msgs
	}
	return msgs[len(msgs)-n:]
}
