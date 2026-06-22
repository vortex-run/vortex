package messaging

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/a2a"
)

// fakeSender records Telegram sends for assertions.
type fakeSender struct {
	mu        sync.Mutex
	messages  []string
	approvals []approvalCall
}

type approvalCall struct {
	desc, approve, reject string
}

func (f *fakeSender) SendMessage(_ context.Context, _ int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, text)
	return nil
}

func (f *fakeSender) SendApprovalRequest(_ context.Context, _ int64, desc, approve, reject string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approvals = append(f.approvals, approvalCall{desc, approve, reject})
	return nil
}

func (f *fakeSender) all() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.messages, "\n---\n")
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.messages)
}

// fakeChat is a directChatter that echoes which agent was asked.
type fakeChat struct {
	mu       sync.Mutex
	lastID   string
	lastMsg  string
	reply    string
	errAgent string
}

func (c *fakeChat) Chat(_ context.Context, agentID, _, message string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastID, c.lastMsg = agentID, message
	if c.errAgent == agentID {
		return "", context.DeadlineExceeded
	}
	return c.reply, nil
}

// fakeCheckpoints is a checkpointResolver recording approve/reject calls.
type fakeCheckpoints struct {
	mu        sync.Mutex
	approved  []string
	rejected  []string
	getResult *a2a.Checkpoint
}

func (c *fakeCheckpoints) Approve(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.approved = append(c.approved, id)
	return nil
}

func (c *fakeCheckpoints) Reject(id, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rejected = append(c.rejected, id)
	return nil
}

func (c *fakeCheckpoints) Get(string) (*a2a.Checkpoint, error) {
	if c.getResult == nil {
		return &a2a.Checkpoint{}, nil
	}
	return c.getResult, nil
}

func TestTeamBridge_MentionRoutesToAgent(t *testing.T) {
	s := &fakeSender{}
	chat := &fakeChat{reply: "I used SQLite for zero-config."}
	b := NewTeamBridge(s, 42, "telegram:42", nil, chat)

	if !b.HandleMention(context.Background(), "@code why sqlite?") {
		t.Fatal("@code should be handled as a mention")
	}
	if chat.lastID != "code-agent" {
		t.Errorf("routed to %q, want code-agent", chat.lastID)
	}
	if chat.lastMsg != "why sqlite?" {
		t.Errorf("message = %q, want 'why sqlite?'", chat.lastMsg)
	}
	if !strings.Contains(s.all(), "SQLite") {
		t.Errorf("reply not forwarded:\n%s", s.all())
	}
}

func TestTeamBridge_MentionAliases(t *testing.T) {
	cases := map[string]string{
		"@coder run it":   "code-agent",
		"@tester check":   "test-agent",
		"@reviewer look":  "review-agent",
		"@coordinator hi": "coordinator",
	}
	for text, want := range cases {
		chat := &fakeChat{reply: "ok"}
		b := NewTeamBridge(&fakeSender{}, 1, "s", nil, chat)
		b.HandleMention(context.Background(), text)
		if chat.lastID != want {
			t.Errorf("%q routed to %q, want %q", text, chat.lastID, want)
		}
	}
}

func TestTeamBridge_NonMentionIgnored(t *testing.T) {
	b := NewTeamBridge(&fakeSender{}, 1, "s", nil, &fakeChat{})
	if b.HandleMention(context.Background(), "build me a server") {
		t.Error("a non-@ message should not be handled as a mention")
	}
}

func TestTeamBridge_UnknownMention(t *testing.T) {
	s := &fakeSender{}
	b := NewTeamBridge(s, 1, "s", nil, &fakeChat{})
	if !b.HandleMention(context.Background(), "@nobody hi") {
		t.Fatal("an @-prefixed message should be consumed even if unknown")
	}
	if !strings.Contains(s.all(), "Unknown agent") {
		t.Errorf("expected unknown-agent reply:\n%s", s.all())
	}
}

func TestTeamBridge_MentionWithoutBody(t *testing.T) {
	s := &fakeSender{}
	b := NewTeamBridge(s, 1, "s", nil, &fakeChat{})
	b.HandleMention(context.Background(), "@code")
	if !strings.Contains(s.all(), "What would you like to ask") {
		t.Errorf("expected prompt for a question:\n%s", s.all())
	}
}

func TestTeamBridge_CheckpointApprove(t *testing.T) {
	s := &fakeSender{}
	cp := &fakeCheckpoints{}
	b := NewTeamBridge(s, 1, "s", cp, nil)

	if !b.Resolve("cp:approve:cp-7") {
		t.Fatal("cp:approve should be consumed")
	}
	if len(cp.approved) != 1 || cp.approved[0] != "cp-7" {
		t.Errorf("approved = %v, want [cp-7]", cp.approved)
	}
	if !strings.Contains(s.all(), "Approved") {
		t.Errorf("expected approval confirmation:\n%s", s.all())
	}
}

func TestTeamBridge_CheckpointReject(t *testing.T) {
	s := &fakeSender{}
	cp := &fakeCheckpoints{}
	b := NewTeamBridge(s, 1, "s", cp, nil)
	b.Resolve("cp:reject:cp-9")
	if len(cp.rejected) != 1 || cp.rejected[0] != "cp-9" {
		t.Errorf("rejected = %v, want [cp-9]", cp.rejected)
	}
}

func TestTeamBridge_NonCheckpointCallbackIgnored(t *testing.T) {
	b := NewTeamBridge(&fakeSender{}, 1, "s", &fakeCheckpoints{}, nil)
	if b.Resolve("approve:session-1") {
		t.Error("a non-cp callback must not be consumed by the team bridge")
	}
}

func TestTeamBridge_CheckpointMessageSurfacesButtons(t *testing.T) {
	s := &fakeSender{}
	cp := &fakeCheckpoints{getResult: &a2a.Checkpoint{
		Files: []a2a.FilePreview{{Path: "main.py", Lines: 12, IsNew: true}},
	}}
	b := NewTeamBridge(s, 1, "sess", cp, nil)

	b.handle(context.Background(), a2a.BusMessage{
		Type: a2a.MsgCheckpoint, SessionID: "sess", From: "code-agent",
		Content: "Code Agent finished.", Metadata: map[string]any{"checkpoint_id": "cp-1"},
	})
	if len(s.approvals) != 1 {
		t.Fatalf("approvals = %d, want 1", len(s.approvals))
	}
	a := s.approvals[0]
	if a.approve != "cp:approve:cp-1" || a.reject != "cp:reject:cp-1" {
		t.Errorf("callback data = %+v", a)
	}
	if !strings.Contains(a.desc, "main.py") {
		t.Errorf("checkpoint files not described:\n%s", a.desc)
	}
}

func TestTeamBridge_ProgressIsBatched(t *testing.T) {
	s := &fakeSender{}
	b := NewTeamBridge(s, 1, "sess", nil, nil)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		b.handle(ctx, a2a.BusMessage{Type: a2a.MsgProgress, SessionID: "sess", From: "code-agent", Content: "step"})
	}
	// Nothing sent yet — progress is queued.
	if s.count() != 0 {
		t.Errorf("progress should be batched, got %d immediate sends", s.count())
	}
	b.flushProgress(ctx)
	if s.count() != 1 {
		t.Fatalf("flush should send one digest, got %d", s.count())
	}
	if !strings.Contains(s.all(), "Progress") || strings.Count(s.all(), "•") != 5 {
		t.Errorf("digest should contain all 5 lines:\n%s", s.all())
	}
}

func TestTeamBridge_OtherSessionFiltered(t *testing.T) {
	s := &fakeSender{}
	b := NewTeamBridge(s, 1, "mine", nil, nil)
	b.handle(context.Background(), a2a.BusMessage{Type: a2a.MsgResult, SessionID: "other", From: "code-agent", Content: "done"})
	if s.count() != 0 {
		t.Errorf("messages from another session must be ignored, got %d", s.count())
	}
}

func TestTeamBridge_ResultFlushesProgress(t *testing.T) {
	s := &fakeSender{}
	b := NewTeamBridge(s, 1, "sess", nil, nil)
	ctx := context.Background()
	b.handle(ctx, a2a.BusMessage{Type: a2a.MsgProgress, SessionID: "sess", From: "code-agent", Content: "working"})
	b.handle(ctx, a2a.BusMessage{Type: a2a.MsgResult, SessionID: "sess", From: "code-agent", Content: "finished"})
	out := s.all()
	if !strings.Contains(out, "Progress") || !strings.Contains(out, "finished") {
		t.Errorf("result should flush queued progress then send the result:\n%s", out)
	}
}

func TestTeamBridge_RunForwardsAndStops(t *testing.T) {
	s := &fakeSender{}
	bus := a2a.NewMessageBus()
	b := NewTeamBridge(s, 1, "sess", nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx, bus); close(done) }()

	// Give the subscription a moment, then publish a task.
	deadline := time.Now().Add(time.Second)
	for s.count() == 0 && time.Now().Before(deadline) {
		bus.Publish(a2a.BusMessage{Type: a2a.MsgTask, SessionID: "sess", From: "coordinator", To: "code-agent", Content: "go"})
		time.Sleep(10 * time.Millisecond)
	}
	if s.count() == 0 {
		t.Error("Run should forward a task message")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop on context cancel")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate short = %q", got)
	}
	if got := truncate("hello world", 5); got != "hello…" {
		t.Errorf("truncate long = %q", got)
	}
}
