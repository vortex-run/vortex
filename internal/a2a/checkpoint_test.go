package a2a

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func sampleFiles() []FilePreview {
	return []FilePreview{
		{Path: "main.py", Content: "print('hi')", Lines: 1, IsNew: true},
		{Path: "auth.py", Content: "def login(): pass", Lines: 1, IsNew: true},
	}
}

// createAsync starts a Create in a goroutine and returns a channel with its
// outcome, plus the checkpoint id once it is registered.
func createAsync(t *testing.T, m *CheckpointManager, session, from, to string) (<-chan *CheckpointOutcome, string) {
	t.Helper()
	out := make(chan *CheckpointOutcome, 1)
	go func() {
		oc, _ := m.Create(session, from, to, *NewResult("task-1", from, true), sampleFiles())
		out <- oc
	}()
	// Wait for the checkpoint to appear in Pending.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p := m.Pending(session); len(p) == 1 {
			return out, p[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("checkpoint never appeared in Pending")
	return nil, ""
}

func TestCheckpoint_BlocksUntilApprove(t *testing.T) {
	m := NewCheckpointManager(nil, 0)
	out, id := createAsync(t, m, "s1", "code-agent", "test-agent")

	// Not resolved yet.
	select {
	case <-out:
		t.Fatal("Create returned before approval")
	case <-time.After(100 * time.Millisecond):
	}

	if err := m.Approve(id); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	select {
	case oc := <-out:
		if oc.Status != CheckpointApproved {
			t.Errorf("status = %q, want approved", oc.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("Create did not return after Approve")
	}
}

func TestCheckpoint_RejectReturnsError(t *testing.T) {
	m := NewCheckpointManager(nil, 0)
	resErr := make(chan error, 1)
	go func() {
		_, err := m.Create("s1", "code-agent", "test-agent", *NewResult("t", "code-agent", true), sampleFiles())
		resErr <- err
	}()
	id := waitPending(t, m, "s1")

	if err := m.Reject(id, "wrong approach"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	select {
	case err := <-resErr:
		if err == nil {
			t.Error("rejected checkpoint should return an error")
		}
	case <-time.After(time.Second):
		t.Fatal("Create did not return after Reject")
	}
}

func TestCheckpoint_EditUnblocksWithFiles(t *testing.T) {
	m := NewCheckpointManager(nil, 0)
	out, id := createAsync(t, m, "s1", "code-agent", "test-agent")

	edits := []FileEdit{{Path: "main.py", NewContent: "print('edited')"}}
	if err := m.Edit(id, edits); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	select {
	case oc := <-out:
		if oc.Status != CheckpointEdited {
			t.Errorf("status = %q, want edited", oc.Status)
		}
		if len(oc.EditedFiles) != 1 || oc.EditedFiles[0].NewContent != "print('edited')" {
			t.Errorf("edited files = %+v", oc.EditedFiles)
		}
		if oc.EditedFiles[0].EditedBy != "user" {
			t.Errorf("EditedBy = %q, want user (defaulted)", oc.EditedFiles[0].EditedBy)
		}
	case <-time.After(time.Second):
		t.Fatal("Create did not return after Edit")
	}
}

func TestCheckpoint_PendingFilters(t *testing.T) {
	m := NewCheckpointManager(nil, 0)
	_, id1 := createAsync(t, m, "s1", "code-agent", "test-agent")
	_, _ = createAsync(t, m, "s2", "code-agent", "test-agent")

	if len(m.Pending("s1")) != 1 {
		t.Errorf("s1 pending = %d, want 1", len(m.Pending("s1")))
	}
	if len(m.Pending("")) != 2 {
		t.Errorf("all pending = %d, want 2", len(m.Pending("")))
	}
	// Resolving removes from pending.
	_ = m.Approve(id1)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(m.Pending("s1")) != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if len(m.Pending("s1")) != 0 {
		t.Error("approved checkpoint should leave Pending")
	}
}

func TestCheckpoint_PublishedToBus(t *testing.T) {
	bus := NewMessageBus()
	ch, unsub := bus.Subscribe()
	defer unsub()
	m := NewCheckpointManager(bus, 0)

	go func() {
		_, _ = m.Create("s1", "code-agent", "test-agent", *NewResult("t", "code-agent", true), sampleFiles())
	}()

	select {
	case msg := <-ch:
		if msg.Type != MsgCheckpoint || msg.From != "code-agent" {
			t.Errorf("checkpoint bus message = %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("checkpoint not published to bus")
	}
}

func TestCheckpoint_AutoApproveAfterTimeout(t *testing.T) {
	m := NewCheckpointManager(nil, 80*time.Millisecond)
	start := time.Now()
	oc, err := m.Create("s1", "code-agent", "test-agent", *NewResult("t", "code-agent", true), sampleFiles())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if oc.Status != CheckpointApproved {
		t.Errorf("status = %q, want auto-approved", oc.Status)
	}
	if time.Since(start) < 70*time.Millisecond {
		t.Error("auto-approve fired too early")
	}
}

func TestCheckpoint_DoubleResolveSafe(t *testing.T) {
	m := NewCheckpointManager(nil, 0)
	_, id := createAsync(t, m, "s1", "code-agent", "test-agent")
	if err := m.Approve(id); err != nil {
		t.Fatalf("first Approve: %v", err)
	}
	// A second resolve must error, not panic / double-close.
	if err := m.Reject(id, "late"); err == nil {
		t.Error("resolving an already-resolved checkpoint should error")
	}
}

func TestCheckpoint_ConcurrentNoDeadlock(t *testing.T) {
	m := NewCheckpointManager(NewMessageBus(), 0)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// Each goroutine uses its own session so it resolves its own
			// checkpoint deterministically.
			session := fmt.Sprintf("s-%d", n)
			out := make(chan *CheckpointOutcome, 1)
			go func() {
				oc, _ := m.Create(session, "code-agent", "test-agent", *NewResult("t", "code-agent", true), sampleFiles())
				out <- oc
			}()
			id := ""
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && id == "" {
				if p := m.Pending(session); len(p) == 1 {
					id = p[0].ID
				} else {
					time.Sleep(2 * time.Millisecond)
				}
			}
			if id != "" {
				_ = m.Approve(id)
			}
			<-out
		}(i)
	}
	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent checkpoints deadlocked")
	}
}

// waitPending blocks until a pending checkpoint exists for session, returning
// its id.
func waitPending(t *testing.T, m *CheckpointManager, session string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p := m.Pending(session); len(p) > 0 {
			return p[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no pending checkpoint")
	return ""
}
