package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/agents"
)

func TestApprovalManager_ApprovedReExecutes(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{AllowedIDs: []int64{7}})
	am := NewApprovalManager(bot, 7, time.Second)

	fn := am.ApprovalFunc()
	done := make(chan bool, 1)
	go func() {
		done <- fn(context.Background(), agents.ApprovalRequest{Command: "go", Args: []string{"build"}})
	}()

	// Wait until the prompt has been sent (a pending approval exists), then
	// approve it. The callback id is appr-1 for the first request.
	waitPending(t, am)
	if !am.Resolve("approve:appr-1") {
		t.Fatal("Resolve should match the pending approval")
	}
	if !<-done {
		t.Error("ApprovalFunc should return true after approval")
	}
}

func TestApprovalManager_Rejected(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{AllowedIDs: []int64{7}})
	am := NewApprovalManager(bot, 7, time.Second)

	fn := am.ApprovalFunc()
	done := make(chan bool, 1)
	go func() {
		done <- fn(context.Background(), agents.ApprovalRequest{Command: "rm"})
	}()
	waitPending(t, am)
	am.Resolve("reject:appr-1")
	if <-done {
		t.Error("ApprovalFunc should return false after rejection")
	}
}

func TestApprovalManager_Timeout(t *testing.T) {
	f := newFakeTelegram(t)
	bot := newTestBot(t, f, TelegramConfig{AllowedIDs: []int64{7}})
	am := NewApprovalManager(bot, 7, 50*time.Millisecond)

	if am.ApprovalFunc()(context.Background(), agents.ApprovalRequest{Command: "go"}) {
		t.Error("ApprovalFunc should return false (reject) on timeout")
	}
}

func TestApprovalManager_NilTelegramDenies(t *testing.T) {
	am := NewApprovalManager(nil, 0, time.Second)
	if am.ApprovalFunc()(context.Background(), agents.ApprovalRequest{Command: "go"}) {
		t.Error("nil telegram should deny (fail safe)")
	}
}

func TestApprovalManager_ResolveUnknownReturnsFalse(t *testing.T) {
	am := NewApprovalManager(nil, 0, time.Second)
	if am.Resolve("approve:nonexistent") {
		t.Error("Resolve of unknown id should return false")
	}
	if am.Resolve("garbage") {
		t.Error("Resolve of malformed data should return false")
	}
}

// waitPending blocks until the manager has at least one pending approval.
func waitPending(t *testing.T, am *ApprovalManager) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		am.mu.Lock()
		n := len(am.pending)
		am.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no pending approval appeared")
}
