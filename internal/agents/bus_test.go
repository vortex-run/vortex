package agents

import (
	"errors"
	"sync"
	"testing"
)

func TestBus_RegisterStoresAgent(t *testing.T) {
	b := NewBus()
	a := NewBaseAgent(AgentConfig{Name: "a", Type: TypeTask})
	if err := b.Register(a); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := b.Agents(); len(got) != 1 || got[0] != "a" {
		t.Errorf("Agents = %v, want [a]", got)
	}
}

func TestBus_RegisterDuplicateErrors(t *testing.T) {
	b := NewBus()
	a := NewBaseAgent(AgentConfig{Name: "a", Type: TypeTask})
	_ = b.Register(a)
	if err := b.Register(a); !errors.Is(err, ErrAlreadyRegistered) {
		t.Errorf("duplicate Register err = %v, want ErrAlreadyRegistered", err)
	}
}

func TestBus_SendRoutesToCorrectAgent(t *testing.T) {
	b := NewBus()
	a1 := NewBaseAgent(AgentConfig{Name: "a1", Type: TypeTask})
	a2 := NewBaseAgent(AgentConfig{Name: "a2", Type: TypeTask})
	_ = b.Register(a1)
	_ = b.Register(a2)

	if err := b.Send(AgentMessage{ToAgent: "a2", ID: "m"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case got := <-a2.Receive():
		if got.ID != "m" {
			t.Errorf("a2 received ID = %q, want m", got.ID)
		}
	default:
		t.Error("a2 should have received the message")
	}
	select {
	case <-a1.Receive():
		t.Error("a1 should not have received the message")
	default:
	}
}

func TestBus_SendUnknownAgentErrors(t *testing.T) {
	b := NewBus()
	if err := b.Send(AgentMessage{ToAgent: "ghost"}); !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("Send to unknown err = %v, want ErrAgentNotFound", err)
	}
}

func TestBus_BroadcastSkipsSender(t *testing.T) {
	b := NewBus()
	sender := NewBaseAgent(AgentConfig{Name: "s", Type: TypeTask})
	r1 := NewBaseAgent(AgentConfig{Name: "r1", Type: TypeTask})
	r2 := NewBaseAgent(AgentConfig{Name: "r2", Type: TypeTask})
	_ = b.Register(sender)
	_ = b.Register(r1)
	_ = b.Register(r2)

	b.Broadcast(AgentMessage{FromAgent: "s", Type: MsgStatus, ID: "b"})

	for _, r := range []*BaseAgent{r1, r2} {
		select {
		case got := <-r.Receive():
			if got.ID != "b" {
				t.Errorf("%s received ID = %q, want b", r.Name(), got.ID)
			}
		default:
			t.Errorf("%s should have received the broadcast", r.Name())
		}
	}
	select {
	case <-sender.Receive():
		t.Error("sender should not receive its own broadcast")
	default:
	}
}

func TestBus_UnregisterRemovesAgent(t *testing.T) {
	b := NewBus()
	a := NewBaseAgent(AgentConfig{Name: "a", Type: TypeTask})
	_ = b.Register(a)
	b.Unregister("a")
	if len(b.Agents()) != 0 {
		t.Errorf("Agents after unregister = %v, want empty", b.Agents())
	}
	if err := b.Send(AgentMessage{ToAgent: "a"}); !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("Send after unregister err = %v, want ErrAgentNotFound", err)
	}
}

func TestBus_StatsMessagesSentIncrements(t *testing.T) {
	b := NewBus()
	a := NewBaseAgent(AgentConfig{Name: "a", Type: TypeTask})
	_ = b.Register(a)
	_ = b.Send(AgentMessage{ToAgent: "a"})
	_ = b.Send(AgentMessage{ToAgent: "a"})
	if s := b.Stats(); s.MessagesSent != 2 {
		t.Errorf("MessagesSent = %d, want 2", s.MessagesSent)
	}
}

func TestBus_StatsMessagesDroppedWhenFull(t *testing.T) {
	b := NewBus()
	a := NewBaseAgent(AgentConfig{Name: "a", Type: TypeTask})
	_ = b.Register(a)
	// Fill the inbox to capacity, then one more to force a drop.
	for i := 0; i < messageBuffer; i++ {
		_ = b.Send(AgentMessage{ToAgent: "a"})
	}
	if err := b.Send(AgentMessage{ToAgent: "a"}); !errors.Is(err, ErrBusFull) {
		t.Fatalf("overflow Send err = %v, want ErrBusFull", err)
	}
	if s := b.Stats(); s.MessagesDropped != 1 {
		t.Errorf("MessagesDropped = %d, want 1", s.MessagesDropped)
	}
}

func TestBus_ConcurrentSendReceiveNoRace(t *testing.T) {
	b := NewBus()
	const n = 8
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		name := string(rune('a' + i))
		a := NewBaseAgent(AgentConfig{Name: name, Type: TypeTask})
		_ = b.Register(a)
		// Drain receiver so the inbox never blocks the senders.
		wg.Add(1)
		go func(rcv <-chan AgentMessage) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				<-rcv
			}
		}(a.Receive())
	}

	for i := 0; i < n; i++ {
		name := string(rune('a' + i))
		wg.Add(1)
		go func(to string) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = b.Send(AgentMessage{ToAgent: to})
			}
		}(name)
	}
	wg.Wait()

	// All sent messages were drained; the bus must report a consistent count.
	if s := b.Stats(); s.AgentCount != n {
		t.Errorf("AgentCount = %d, want %d", s.AgentCount, n)
	}
}
