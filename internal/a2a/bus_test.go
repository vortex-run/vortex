package a2a

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestBus_PublishAppendsHistory(t *testing.T) {
	b := NewMessageBus()
	b.Publish(BusMessage{From: "coordinator", To: "code-agent", Type: MsgTask, Content: "write code", SessionID: "s1"})
	b.Publish(BusMessage{From: "code-agent", To: "coordinator", Type: MsgResult, Content: "done", SessionID: "s1"})
	hist := b.History("", 0)
	if len(hist) != 2 {
		t.Fatalf("history = %d, want 2", len(hist))
	}
	// IDs and timestamps are filled in.
	if hist[0].ID == "" || hist[0].Timestamp.IsZero() {
		t.Error("Publish should fill in ID and timestamp")
	}
}

func TestBus_SubscribeReceives(t *testing.T) {
	b := NewMessageBus()
	ch, unsub := b.Subscribe()
	defer unsub()

	b.Publish(BusMessage{From: "a", To: "b", Content: "hi", SessionID: "s1"})
	select {
	case m := <-ch:
		if m.Content != "hi" {
			t.Errorf("received %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the message")
	}
}

func TestBus_MultipleSubscribers(t *testing.T) {
	b := NewMessageBus()
	ch1, u1 := b.Subscribe()
	ch2, u2 := b.Subscribe()
	defer u1()
	defer u2()

	b.Publish(BusMessage{Content: "broadcast"})
	for i, ch := range []<-chan BusMessage{ch1, ch2} {
		select {
		case m := <-ch:
			if m.Content != "broadcast" {
				t.Errorf("sub %d got %+v", i, m)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d missed the message", i)
		}
	}
}

func TestBus_Unsubscribe(t *testing.T) {
	b := NewMessageBus()
	ch, unsub := b.Subscribe()
	unsub()
	// The channel is closed; a receive returns the zero value, not open.
	if _, open := <-ch; open {
		t.Error("unsubscribed channel should be closed")
	}
	// Publishing after unsubscribe must not panic (no send on closed channel).
	b.Publish(BusMessage{Content: "after"})
}

func TestBus_HistoryFiltersBySession(t *testing.T) {
	b := NewMessageBus()
	b.Publish(BusMessage{Content: "a", SessionID: "s1"})
	b.Publish(BusMessage{Content: "b", SessionID: "s2"})
	b.Publish(BusMessage{Content: "c", SessionID: "s1"})
	s1 := b.History("s1", 0)
	if len(s1) != 2 {
		t.Errorf("s1 history = %d, want 2", len(s1))
	}
	for _, m := range s1 {
		if m.SessionID != "s1" {
			t.Errorf("unexpected session in s1 history: %+v", m)
		}
	}
}

func TestBus_HistoryLimit(t *testing.T) {
	b := NewMessageBus()
	for i := 0; i < 10; i++ {
		b.Publish(BusMessage{Content: fmt.Sprintf("m%d", i), SessionID: "s1"})
	}
	last3 := b.History("s1", 3)
	if len(last3) != 3 {
		t.Fatalf("limit history = %d, want 3", len(last3))
	}
	if last3[2].Content != "m9" {
		t.Errorf("last message = %q, want m9", last3[2].Content)
	}
}

func TestBus_AgentMessages(t *testing.T) {
	b := NewMessageBus()
	b.Publish(BusMessage{From: "coordinator", To: "code-agent", Content: "task"})
	b.Publish(BusMessage{From: "code-agent", To: "coordinator", Content: "result"})
	b.Publish(BusMessage{From: "coordinator", To: "test-agent", Content: "other"})
	msgs := b.AgentMessages("code-agent", 0)
	if len(msgs) != 2 {
		t.Errorf("code-agent messages = %d, want 2", len(msgs))
	}
}

func TestBus_SlowSubscriberDoesNotBlock(t *testing.T) {
	b := NewMessageBus()
	// A subscriber that never drains; its buffer fills and further messages
	// drop, but Publish must keep returning promptly.
	_, unsub := b.Subscribe()
	defer unsub()

	done := make(chan struct{})
	go func() {
		for i := 0; i < busSubBuffer*3; i++ {
			b.Publish(BusMessage{Content: fmt.Sprintf("m%d", i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
}

func TestBus_ConcurrentPublish(t *testing.T) {
	b := NewMessageBus()
	ch, unsub := b.Subscribe()
	defer unsub()
	// Drain in the background so the subscriber never blocks publishers.
	var drained int
	var dmu sync.Mutex
	go func() {
		for range ch {
			dmu.Lock()
			drained++
			dmu.Unlock()
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			b.Publish(BusMessage{Content: fmt.Sprintf("m%d", n), SessionID: "s1"})
		}(i)
	}
	wg.Wait()
	if len(b.History("s1", 0)) != 50 {
		t.Errorf("history = %d, want 50", len(b.History("s1", 0)))
	}
	unsub()
	time.Sleep(20 * time.Millisecond)
	dmu.Lock()
	got := drained
	dmu.Unlock()
	if got == 0 {
		t.Error("drain goroutine should have observed messages")
	}
}
