package agents

import (
	"context"
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
