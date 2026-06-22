package a2a

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// chatStub is a stubAgent that also implements Chatter, recording the system
// context it receives.
type chatStub struct {
	*stubAgent
	mu          sync.Mutex
	reply       string
	lastContext string
	lastMsg     string
	calls       int
}

func (c *chatStub) Chat(_ context.Context, taskContext, userMessage string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.lastContext = taskContext
	c.lastMsg = userMessage
	return c.reply, nil
}

func newChatStub(id, reply string) *chatStub {
	return &chatStub{stubAgent: newStub(id, "coder"), reply: reply}
}

func TestDirectChat_SendCallsAgent(t *testing.T) {
	agent := newChatStub("code-agent", "I used SQLite because the project already uses it.")
	dc := NewDirectChat(agent, nil)

	reply, err := dc.Send(context.Background(), "s1", "why did you use sqlite?")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(reply, "SQLite") {
		t.Errorf("reply = %q", reply)
	}
	if agent.lastMsg != "why did you use sqlite?" {
		t.Errorf("agent got message %q", agent.lastMsg)
	}
}

func TestDirectChat_NonChatterErrors(t *testing.T) {
	// A plain stubAgent does not implement Chatter.
	dc := NewDirectChat(newStub("code-agent", "coder"), nil)
	if _, err := dc.Send(context.Background(), "s1", "hi"); err == nil {
		t.Error("Send to a non-Chatter agent should error")
	}
}

func TestDirectChat_PublishesToBus(t *testing.T) {
	bus := NewMessageBus()
	ch, unsub := bus.Subscribe()
	defer unsub()
	dc := NewDirectChat(newChatStub("code-agent", "sure"), bus)

	go func() { _, _ = dc.Send(context.Background(), "s1", "do the thing") }()

	// Expect a user message then an agent reply, both direct-chat typed.
	var sawUser, sawAgent bool
	for i := 0; i < 2; i++ {
		select {
		case m := <-ch:
			if m.Type != MsgDirectChat {
				t.Errorf("bus message type = %q, want direct-chat", m.Type)
			}
			if m.From == "user" {
				sawUser = true
			}
			if m.From == "code-agent" {
				sawAgent = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("missing direct-chat bus message")
		}
	}
	if !sawUser || !sawAgent {
		t.Errorf("expected both user and agent bus messages (user=%v agent=%v)", sawUser, sawAgent)
	}
}

func TestDirectChat_History(t *testing.T) {
	dc := NewDirectChat(newChatStub("code-agent", "ack"), nil)
	_, _ = dc.Send(context.Background(), "s1", "first")
	_, _ = dc.Send(context.Background(), "s1", "second")
	hist := dc.History("s1")
	// 2 user + 2 agent turns.
	if len(hist) != 4 {
		t.Fatalf("history = %d turns, want 4", len(hist))
	}
	if hist[0].Role != "user" || hist[0].Content != "first" || hist[1].Role != "agent" {
		t.Errorf("history order wrong: %+v", hist)
	}
}

func TestDirectChat_SessionsIsolated(t *testing.T) {
	dc := NewDirectChat(newChatStub("code-agent", "ack"), nil)
	_, _ = dc.Send(context.Background(), "s1", "a")
	_, _ = dc.Send(context.Background(), "s2", "b")
	if len(dc.History("s1")) != 2 || len(dc.History("s2")) != 2 {
		t.Error("sessions should be isolated")
	}
	if dc.History("unknown") != nil {
		t.Error("unknown session should be nil")
	}
}

func TestDirectChat_ContextCarriedForward(t *testing.T) {
	agent := newChatStub("code-agent", "ok")
	dc := NewDirectChat(agent, nil)
	_, _ = dc.Send(context.Background(), "s1", "first question")
	_, _ = dc.Send(context.Background(), "s1", "follow up")
	// The second call's context includes the first exchange.
	agent.mu.Lock()
	ctx := agent.lastContext
	agent.mu.Unlock()
	if !strings.Contains(ctx, "first question") {
		t.Errorf("follow-up context missing prior turn:\n%s", ctx)
	}
}

func TestDirectChat_ConcurrentSafe(t *testing.T) {
	dc := NewDirectChat(newChatStub("code-agent", "ok"), nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = dc.Send(context.Background(), "s1", "msg")
		}()
	}
	wg.Wait()
	if len(dc.History("s1")) != 40 { // 20 user + 20 agent
		t.Errorf("history = %d, want 40", len(dc.History("s1")))
	}
}
