//go:build release

package agents

import (
	"context"
	"errors"
	"testing"
)

// These tests compile only under the `release` build tag — the tag shipped
// binaries are built with. They assert the property audit I6 asks for: in a
// release build, command execution cannot happen without a human approval,
// no matter how the tool was constructed.
//
// Run with: go test -tags release ./internal/agents/

func TestRelease_RunCommandGateCannotBeWaived(t *testing.T) {
	var approvalErr *ApprovalError

	// The zero-value hazard: a struct literal that forgets RequireApproval.
	// In a dev build this executes; in a release build it must not.
	bare := RunCommandTool{SandboxDir: t.TempDir(), AllowedCommands: []string{"go"}}
	_, err := bare.Execute(context.Background(), map[string]any{
		"command": "go", "args": []string{"version"},
	})
	if !errors.As(err, &approvalErr) {
		t.Errorf("zero-valued RequireApproval executed in a release build (err=%v); the gate must be unwaivable", err)
	}

	// An explicit waiver must be equally ineffective.
	waived := RunCommandTool{SandboxDir: t.TempDir(), AllowedCommands: []string{"go"}, RequireApproval: false}
	_, err = waived.Execute(context.Background(), map[string]any{
		"command": "go", "args": []string{"version"},
	})
	if !errors.As(err, &approvalErr) {
		t.Errorf("explicit RequireApproval=false executed in a release build (err=%v)", err)
	}
}

func TestRelease_RunTerminalGateCannotBeWaived(t *testing.T) {
	var approvalErr *ApprovalError
	tool := RunTerminalTool{cfg: LocalFSConfig{Root: t.TempDir()}, RequireApproval: false}
	_, err := tool.Execute(context.Background(), map[string]any{"command": "go version"})
	if !errors.As(err, &approvalErr) {
		t.Errorf("run_terminal executed with the gate waived in a release build (err=%v)", err)
	}
}

// TestRelease_ApprovedExecutionStillRuns is the other half of the property:
// the gate must remain unwaivable WITHOUT breaking the approval flow, or a
// release binary could never run an approved command at all.
func TestRelease_ApprovedExecutionStillRuns(t *testing.T) {
	reg := NewToolRegistry()
	if err := reg.Register(NewRunCommandTool(t.TempDir(), []string{"go"})); err != nil {
		t.Fatal(err)
	}
	sandboxed := NewSandboxedRegistry(reg, t.TempDir(), []string{"go"}, nil)

	// Unapproved: gated.
	var approvalErr *ApprovalError
	if _, err := sandboxed.Execute(context.Background(), "run_command", map[string]any{
		"command": "go", "args": []string{"version"},
	}); !errors.As(err, &approvalErr) {
		t.Fatalf("expected an approval gate before approval, got %v", err)
	}

	// After approval: runs.
	res, err := sandboxed.ExecuteApproved(context.Background(), "run_command", map[string]any{
		"command": "go", "args": []string{"version"},
	})
	if err != nil {
		t.Fatalf("approved execution failed in a release build: %v", err)
	}
	out, ok := res.(map[string]any)
	if !ok || out["exit_code"] != 0 {
		t.Errorf("approved execution result = %#v, want exit_code 0", res)
	}
}
