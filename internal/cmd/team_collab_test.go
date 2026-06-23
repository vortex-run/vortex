package cmd

import (
	"context"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/a2a"
	"github.com/vortex-run/vortex/internal/agents"
)

// newCollab builds a teamCollab over a real bus + checkpoint manager (no agents
// registered) for adapter-level assertions.
func newCollab() *teamCollab {
	bus := a2a.NewMessageBus()
	return &teamCollab{
		server:      a2a.NewAgentServer(),
		bus:         bus,
		checkpoints: a2a.NewCheckpointManager(bus, 0),
	}
}

func TestTeamCollab_HistoryMapsBusMessages(t *testing.T) {
	c := newCollab()
	c.bus.Publish(a2a.BusMessage{From: "coordinator", To: "code-agent", Type: a2a.MsgTask, Content: "go", SessionID: "s"})
	c.bus.Publish(a2a.BusMessage{From: "code-agent", To: "coordinator", Type: a2a.MsgResult, Content: "done", SessionID: "s"})

	recs := c.History(10)
	if len(recs) != 2 {
		t.Fatalf("history = %d, want 2", len(recs))
	}
	if recs[0].From != "coordinator" || recs[0].Type != a2a.MsgTask || recs[0].Content != "go" {
		t.Errorf("first record = %+v", recs[0])
	}
	if recs[0].ID == "" || recs[0].Timestamp.IsZero() {
		t.Error("record should carry the bus-assigned ID and timestamp")
	}
}

func TestTeamCollab_SubscribeForwardsLive(t *testing.T) {
	c := newCollab()
	ch, unsub := c.Subscribe()
	defer unsub()

	c.bus.Publish(a2a.BusMessage{From: "test-agent", Type: a2a.MsgProgress, Content: "running"})
	select {
	case rec := <-ch:
		if rec.From != "test-agent" || rec.Content != "running" {
			t.Errorf("forwarded record = %+v", rec)
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not forward a live message")
	}
}

func TestTeamCollab_ChatUnknownAgent(t *testing.T) {
	c := newCollab()
	if _, err := c.Chat(context.Background(), "ghost", "s", "hi"); err == nil {
		t.Error("Chat to an unregistered agent should error")
	}
}

func TestTeamCollab_ChatCoordinatorRoutesToCoordinator(t *testing.T) {
	// With a real coordinator wired, "coordinator" must NOT fall through to the
	// A2A DirectChatFor lookup (which would 502) — it routes to HandleMessage.
	c := newCollab()
	coord, err := agents.NewCoordinator(agents.CoordinatorConfig{
		Bus:       agents.NewBus(),
		AIGateway: agents.StubAIGateway{AnswerReply: "I coordinate the team."},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.coordinator = coord

	reply, err := c.Chat(context.Background(), "coordinator", "s1", "what can you do?")
	if err != nil {
		t.Fatalf("coordinator chat errored (the 502 bug): %v", err)
	}
	if reply == "" {
		t.Error("coordinator chat returned an empty reply")
	}
}

func TestTeamCollab_ChatCoordinatorNilErrors(t *testing.T) {
	c := newCollab() // no coordinator wired
	if _, err := c.Chat(context.Background(), "coordinator", "s", "hi"); err == nil {
		t.Error("coordinator chat with no coordinator should error cleanly, not panic")
	}
}

func TestTeamCollab_ListPendingCheckpoints(t *testing.T) {
	c := newCollab()
	// Create a checkpoint asynchronously (Create blocks until resolved).
	created := make(chan struct{})
	go func() {
		close(created)
		_, _ = c.checkpoints.Create("s", "code-agent", "test-agent",
			a2a.TaskResult{}, []a2a.FilePreview{{Path: "main.py", Lines: 9, IsNew: true}})
	}()
	<-created

	// Wait for it to register as pending.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if recs := c.List(); len(recs) == 1 {
			if recs[0].FromAgent != "code-agent" || recs[0].ToAgent != "test-agent" {
				t.Errorf("checkpoint = %+v", recs[0])
			}
			if len(recs[0].Files) != 1 || recs[0].Files[0].Path != "main.py" {
				t.Errorf("checkpoint files = %+v", recs[0].Files)
			}
			// Approve to unblock the goroutine.
			if err := c.Approve(recs[0].ID); err != nil {
				t.Errorf("approve: %v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("checkpoint never became pending")
}

func TestPrimaryTelegramChatID(t *testing.T) {
	cases := []struct {
		env  string
		want int64
		ok   bool
	}{
		{"", 0, false},
		{"123456", 123456, true},
		{"111, 222", 111, true},
		{"  789  ", 789, true},
		{"notanumber", 0, false},
	}
	for _, tc := range cases {
		t.Setenv("VORTEX_TELEGRAM_ALLOWED_IDS", tc.env)
		got, ok := primaryTelegramChatID()
		if ok != tc.ok || got != tc.want {
			t.Errorf("primaryTelegramChatID(%q) = (%d,%v), want (%d,%v)", tc.env, got, ok, tc.want, tc.ok)
		}
	}
}
