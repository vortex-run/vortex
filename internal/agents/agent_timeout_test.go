package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAgentRun_OutlivesTheChatDeadline: an /agent run gets AgentRunTimeout,
// not the 120s chat deadline that used to cut real runs off mid-task.
func TestAgentRun_OutlivesTheChatDeadline(t *testing.T) {
	var left time.Duration
	c, _ := agentCoord(t, toolGateway{writeScript()}, func(ctx context.Context, _ ApprovalRequest) bool {
		if d, ok := ctx.Deadline(); ok {
			left = time.Until(d)
		}
		return true
	})
	if _, err := c.HandleMessage(context.Background(), "/agent create hello.txt", "t1"); err != nil {
		t.Fatal(err)
	}
	if left <= handleMessageTimeout || left > AgentRunTimeout {
		t.Errorf("time left in the run = %v, want between %v and %v", left, handleMessageTimeout, AgentRunTimeout)
	}
}

// TestAgentRun_StopsWhenTheCallerGoesAway: with nobody left to approve or
// read the answer, a waiting run ends and its pending action is withdrawn.
func TestAgentRun_StopsWhenTheCallerGoesAway(t *testing.T) {
	c, dir := agentCoord(t, toolGateway{writeScript()}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.HandleMessage(ctx, "/agent create hello.txt", "t2")
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !c.HasPendingApproval("t2") {
		if time.Now().After(deadline) {
			t.Fatal("loop never asked for approval")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run kept going after the caller left")
	}
	if c.HasPendingApproval("t2") {
		t.Error("pending approval left behind")
	}
	if _, err := os.Stat(filepath.Join(dir, "hello.txt")); err == nil {
		t.Error("file was written")
	}
}

// TestChat_StillIgnoresTheCallerContext: ordinary messages keep their own
// deadline, so a cancelled caller doesn't abort them (unchanged behaviour).
func TestChat_StillIgnoresTheCallerContext(t *testing.T) {
	c, _ := agentCoord(t, toolGateway{writeScript()}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.HandleMessage(ctx, "/ls", "t3"); err != nil {
		t.Fatalf("chat with a cancelled caller: %v", err)
	}
}
