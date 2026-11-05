package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// approvalAuditRecorder captures audit entries.
type approvalAuditRecorder struct {
	mu      sync.Mutex
	entries []string // action|resource
	details []map[string]any
}

func (r *approvalAuditRecorder) Append(_ context.Context, _, action, resource string, detail map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, action+"|"+resource)
	r.details = append(r.details, detail)
	return nil
}

func auditedCoord(t *testing.T, approval ApprovalFunc) (*Coordinator, *approvalAuditRecorder) {
	t.Helper()
	c, _ := localCoord(t, approval)
	rec := &approvalAuditRecorder{}
	c.cfg.Audit = rec
	return c, rec
}

func wantEntries(t *testing.T, rec *approvalAuditRecorder, want ...string) {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.entries) != len(want) {
		t.Fatalf("audit entries = %v, want %v", rec.entries, want)
	}
	for i := range want {
		if rec.entries[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, rec.entries[i], want[i])
		}
	}
}

// TestApprovalAudit_SyncApprover: request, decision and execution of an
// approval-gated action are all in the audit log, not only the process log.
func TestApprovalAudit_SyncApprover(t *testing.T) {
	c, rec := auditedCoord(t, func(context.Context, ApprovalRequest) bool { return true })
	if _, err := c.ExecuteLocalTool(context.Background(), "s1", "write_file",
		map[string]any{"path": "a.txt", "content": "x"}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	wantEntries(t, rec, "agent.approval.requested|write_file", "agent.approval.decided|write_file", "agent.action.executed|write_file")
	if rec.details[1]["approved"] != true || rec.details[2]["ok"] != true || rec.details[0]["session"] != "s1" {
		t.Errorf("details = %v", rec.details)
	}
}

func TestApprovalAudit_SyncRejectNotExecuted(t *testing.T) {
	c, rec := auditedCoord(t, func(context.Context, ApprovalRequest) bool { return false })
	_, _ = c.ExecuteLocalTool(context.Background(), "s1", "write_file",
		map[string]any{"path": "a.txt", "content": "x"}, func(string) {})
	wantEntries(t, rec, "agent.approval.requested|write_file", "agent.approval.decided|write_file")
	if rec.details[1]["approved"] != false {
		t.Errorf("decision detail = %v", rec.details[1])
	}
}

func TestApprovalAudit_AsyncApprove(t *testing.T) {
	c, rec := auditedCoord(t, nil)
	_, _ = c.ExecuteLocalTool(context.Background(), "s2", "write_file",
		map[string]any{"path": "b.txt", "content": "y"}, func(string) {})
	if _, matched := c.ApproveAction("s2", true); !matched {
		t.Fatal("no pending action")
	}
	wantEntries(t, rec, "agent.approval.requested|write_file", "agent.approval.decided|write_file", "agent.action.executed|write_file")
	if rec.details[1]["via"] != "api" || rec.details[1]["action"] == "" {
		t.Errorf("async decision detail = %v", rec.details[1])
	}
}

func TestApprovalAudit_ReadOnlyNotAudited(t *testing.T) {
	c, rec := auditedCoord(t, nil)
	_, _ = c.ExecuteLocalTool(context.Background(), "s3", "list_directory", map[string]any{"path": "."}, func(string) {})
	wantEntries(t, rec)
}

// TestApproval_ExpiredIsNotExecuted: an approval arriving after approvalTTL
// must not run the stashed action.
func TestApproval_ExpiredIsNotExecuted(t *testing.T) {
	c, rec := auditedCoord(t, nil)
	_, _ = c.ExecuteLocalTool(context.Background(), "old", "write_file",
		map[string]any{"path": "late.txt", "content": "x"}, func(string) {})
	c.mu.Lock()
	c.pending["old"].created = time.Now().Add(-approvalTTL - time.Minute)
	c.mu.Unlock()

	if c.HasPendingApproval("old") {
		t.Error("an expired action is still reported as pending")
	}
	transcript, matched := c.ApproveAction("old", true)
	if !matched || !strings.Contains(transcript, "expired") {
		t.Fatalf("ApproveAction = %q, %v; want an expiry message", transcript, matched)
	}
	if _, err := os.Stat(filepath.Join(c.WorkingDir(), "late.txt")); err == nil {
		t.Error("expired approval executed the action")
	}
	wantEntries(t, rec, "agent.approval.requested|write_file", "agent.approval.expired|write_file")
}

// TestApproval_BoundToActionID: an approve call naming another action's id
// is refused and leaves the pending action alone; the right id executes it.
func TestApproval_BoundToActionID(t *testing.T) {
	c, dir := agentCoord(t, toolGateway{writeScript()}, nil)
	done := make(chan string, 1)
	go func() {
		reply, _ := c.HandleMessage(context.Background(), "/agent create hello.txt", "b1")
		done <- reply
	}()
	for i := 0; !c.HasPendingApproval("b1"); i++ {
		if i > 500 {
			t.Fatal("no approval request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.mu.Lock()
	id := c.pending["b1"].actionID
	c.mu.Unlock()
	if len(id) != 16 {
		t.Fatalf("action id = %q", id)
	}
	if _, matched, err := c.ApproveActionID("b1", "0000000000000000", true); !matched || !errors.Is(err, ErrActionMismatch) {
		t.Fatalf("stale id: matched=%v err=%v", matched, err)
	}
	if !c.HasPendingApproval("b1") {
		t.Fatal("mismatched approval consumed the pending action")
	}
	if _, err := os.Stat(filepath.Join(dir, "hello.txt")); err == nil {
		t.Fatal("file written on a mismatched approval")
	}
	if _, matched, err := c.ApproveActionID("b1", id, true); !matched || err != nil {
		t.Fatalf("right id: matched=%v err=%v", matched, err)
	}
	if reply := <-done; !strings.Contains(reply, "(action "+id+")") {
		t.Errorf("approval line lacks the id: %q", reply)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "hello.txt")); string(data) != "hi" {
		t.Errorf("file = %q", data)
	}
}

func TestActionID_DependsOnWhatRuns(t *testing.T) {
	a := ApprovalRequest{Tool: "run_terminal", Description: "run ls", Params: map[string]any{"command": "ls"}}
	b := a
	b.Params = map[string]any{"command": "rm -rf ."}
	if ActionID(a) == ActionID(b) {
		t.Error("different commands share an id")
	}
	if ActionID(a) != ActionID(ApprovalRequest{Tool: "run_terminal", Description: "run ls", Params: map[string]any{"command": "ls"}}) {
		t.Error("id is not deterministic")
	}
}

// TestApproval_LoopDecisionAuditedOnce: an /agent approval resolved through
// the API is recorded once (by the loop), not once per layer.
func TestApproval_LoopDecisionAuditedOnce(t *testing.T) {
	c, _ := agentCoord(t, toolGateway{writeScript()}, nil)
	rec := &approvalAuditRecorder{}
	c.cfg.Audit = rec
	done := make(chan struct{})
	go func() { _, _ = c.HandleMessage(context.Background(), "/agent create hello.txt", "once"); close(done) }()
	for i := 0; !c.HasPendingApproval("once"); i++ {
		if i > 500 {
			t.Fatal("no approval request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.ApproveAction("once", true)
	<-done
	rec.mu.Lock()
	defer rec.mu.Unlock()
	n := map[string]int{}
	for _, e := range rec.entries {
		n[e]++
	}
	if n["agent.approval.requested|write_file"] != 1 || n["agent.approval.decided|write_file"] != 1 || n["agent.action.executed|write_file"] != 1 {
		t.Errorf("audit = %v", rec.entries)
	}
}
