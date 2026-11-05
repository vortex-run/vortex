package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandeepbollavaram/vortex/internal/llm"
)

// scriptedModel replays canned replies and records what it was sent.
type scriptedModel struct {
	replies []llm.ToolChatResult
	err     error
	seen    [][]llm.ChatMessage
	tools   []llm.ToolDef
	system  string
}

func (m *scriptedModel) ChatWithTools(_ context.Context, system string, msgs []llm.ChatMessage, tools []llm.ToolDef) (llm.ToolChatResult, error) {
	m.seen = append(m.seen, append([]llm.ChatMessage(nil), msgs...))
	m.tools = tools
	m.system = system
	if m.err != nil {
		return llm.ToolChatResult{}, m.err
	}
	if len(m.replies) == 0 {
		return llm.ToolChatResult{Text: "done"}, nil
	}
	r := m.replies[0]
	m.replies = m.replies[1:]
	return r, nil
}

func call(id, name string, args map[string]any) llm.ToolChatResult {
	return llm.ToolChatResult{ToolCalls: []llm.ToolCall{{ID: id, Name: name, Arguments: args}}}
}

func loopWorkspace(t *testing.T) (*ToolRegistry, string) {
	t.Helper()
	dir := t.TempDir()
	reg := NewToolRegistry()
	if err := RegisterLocalTools(reg, LocalFSConfig{Root: dir}); err != nil {
		t.Fatal(err)
	}
	return reg, dir
}

// lastToolResult returns the tool-result content the model saw on its last turn.
func lastToolResult(m *scriptedModel) string {
	msgs := m.seen[len(m.seen)-1]
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "tool" {
			return msgs[i].Content
		}
	}
	return ""
}

func TestRunLoop_ReadThenAnswer(t *testing.T) {
	reg, dir := loopWorkspace(t)
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module demo"), 0o600)
	m := &scriptedModel{replies: []llm.ToolChatResult{
		call("c1", "read_file", map[string]any{"path": "go.mod"}),
		{Text: "The module is demo."},
	}}
	res := RunLoop(context.Background(), LoopConfig{Model: m, Tools: reg}, "what module is this?")
	if res.Stop != LoopDone || res.Answer != "The module is demo." || len(res.Steps) != 1 || res.Steps[0].Outcome != "ok" {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(lastToolResult(m), "module demo") {
		t.Errorf("model didn't see the file content: %q", lastToolResult(m))
	}
	if len(m.tools) == 0 {
		t.Error("no tools were advertised to the model")
	}
}

func TestRunLoop_InvalidArgsAreReportedAndRecoverable(t *testing.T) {
	reg, dir := loopWorkspace(t)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("A"), 0o600)
	m := &scriptedModel{replies: []llm.ToolChatResult{
		call("c1", "read_file", map[string]any{"file": "a.txt"}), // wrong field
		call("c2", "read_file", map[string]any{"path": "a.txt"}),
		{Text: "A"},
	}}
	res := RunLoop(context.Background(), LoopConfig{Model: m, Tools: reg}, "read a.txt")
	if res.Stop != LoopDone || len(res.Steps) != 2 || res.Steps[0].Outcome != "invalid" || res.Steps[1].Outcome != "ok" {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if !strings.Contains(m.seen[1][len(m.seen[1])-1].Content, "missing required argument") {
		t.Errorf("model wasn't told why the call was invalid: %+v", m.seen[1])
	}
}

func TestRunLoop_UnknownTool(t *testing.T) {
	reg, _ := loopWorkspace(t)
	m := &scriptedModel{replies: []llm.ToolChatResult{call("c1", "rm_rf", map[string]any{})}}
	res := RunLoop(context.Background(), LoopConfig{Model: m, Tools: reg}, "x")
	if res.Steps[0].Outcome != "unknown_tool" {
		t.Fatalf("steps = %+v", res.Steps)
	}
}

func TestRunLoop_WriteNeedsApproval(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approval ApprovalFunc
		want     string
		written  bool
	}{
		{"approved", func(context.Context, ApprovalRequest) bool { return true }, "ok", true},
		{"rejected", func(context.Context, ApprovalRequest) bool { return false }, "rejected", false},
		{"no approver", nil, "rejected", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, dir := loopWorkspace(t)
			audit := &approvalAuditRecorder{}
			m := &scriptedModel{replies: []llm.ToolChatResult{
				call("c1", "write_file", map[string]any{"path": "out.txt", "content": "hi"}),
			}}
			res := RunLoop(context.Background(), LoopConfig{Model: m, Tools: reg, Approval: tc.approval, Audit: audit, Session: "s"}, "write it")
			if res.Steps[0].Outcome != tc.want {
				t.Fatalf("outcome = %s, want %s (%+v)", res.Steps[0].Outcome, tc.want, res.Steps[0])
			}
			_, err := os.Stat(filepath.Join(dir, "out.txt"))
			if (err == nil) != tc.written {
				t.Errorf("file written = %v, want %v", err == nil, tc.written)
			}
			if len(audit.entries) < 2 || audit.entries[0] != "agent.approval.requested|write_file" ||
				audit.entries[1] != "agent.approval.decided|write_file" {
				t.Errorf("audit = %v", audit.entries)
			}
		})
	}
}

func TestRunLoop_ModelCannotSupplySessionID(t *testing.T) {
	reg, _ := loopWorkspace(t)
	m := &scriptedModel{replies: []llm.ToolChatResult{
		call("c1", "list_directory", map[string]any{"path": ".", "session_id": "someone-else"}),
	}}
	res := RunLoop(context.Background(), LoopConfig{Model: m, Tools: reg, Session: "mine"}, "ls")
	if res.Steps[0].Outcome != "invalid" {
		t.Errorf("a model-supplied session_id must be rejected: %+v", res.Steps[0])
	}
}

func TestRunLoop_StepBudget(t *testing.T) {
	reg, _ := loopWorkspace(t)
	var replies []llm.ToolChatResult
	for i := 0; i < 10; i++ {
		replies = append(replies, call("c", "list_directory", map[string]any{}))
	}
	m := &scriptedModel{replies: replies}
	res := RunLoop(context.Background(), LoopConfig{Model: m, Tools: reg, MaxSteps: 3}, "loop forever")
	if res.Stop != LoopBudgetExceeded || res.Turns != 3 || res.Err == nil {
		t.Fatalf("res = %+v", res)
	}
}

func TestRunLoop_ModelError(t *testing.T) {
	reg, _ := loopWorkspace(t)
	m := &scriptedModel{err: errors.New("provider down")}
	res := RunLoop(context.Background(), LoopConfig{Model: m, Tools: reg}, "x")
	if res.Stop != LoopModelError || res.Err == nil {
		t.Fatalf("res = %+v", res)
	}
}

func TestRunLoop_ObservationIsBounded(t *testing.T) {
	reg, dir := loopWorkspace(t)
	_ = os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("x", 5000)), 0o600)
	m := &scriptedModel{replies: []llm.ToolChatResult{call("c1", "read_file", map[string]any{"path": "big.txt"}), {Text: "ok"}}}
	RunLoop(context.Background(), LoopConfig{Model: m, Tools: reg, MaxObservation: 1000}, "read big")
	got := lastToolResult(m)
	if len(got) > 1100 || !strings.Contains(got, "output truncated") {
		t.Errorf("observation not bounded: %d bytes", len(got))
	}
}

func TestLoop_TokenBudgetStopsBeforeMoreActions(t *testing.T) {
	reg, dir := loopWorkspace(t)
	r1 := call("c1", "list_directory", map[string]any{"path": "."})
	r1.Tokens = 600
	r2 := call("c2", "write_file", map[string]any{"path": "x.txt", "content": "x"})
	r2.Tokens = 600
	m := &scriptedModel{replies: []llm.ToolChatResult{r1, r2}}
	approve := func(context.Context, ApprovalRequest) bool { return true }
	res := RunLoop(context.Background(), LoopConfig{Model: m, Tools: reg, Approval: approve, MaxTokens: 1000}, "go")
	if res.Stop != LoopBudgetExceeded || res.Tokens != 1200 || len(res.Steps) != 1 {
		t.Fatalf("stop=%s tokens=%d steps=%d", res.Stop, res.Tokens, len(res.Steps))
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); err == nil {
		t.Error("write ran after the token budget was spent")
	}
}

func TestLoop_CallsPerTurnCapped(t *testing.T) {
	reg, _ := loopWorkspace(t)
	var calls []llm.ToolCall
	for i := 0; i < 5; i++ {
		calls = append(calls, llm.ToolCall{ID: fmt.Sprint("c", i), Name: "list_directory", Arguments: map[string]any{"path": "."}})
	}
	m := &scriptedModel{replies: []llm.ToolChatResult{{ToolCalls: calls}, {Text: "ok"}}}
	res := RunLoop(context.Background(), LoopConfig{Model: m, Tools: reg, MaxCallsPerTurn: 2}, "go")
	var ran, skipped int
	for _, s := range res.Steps {
		if s.Outcome == "skipped" {
			skipped++
		} else {
			ran++
		}
	}
	if ran != 2 || skipped != 3 || res.Stop != LoopDone {
		t.Errorf("ran=%d skipped=%d stop=%s", ran, skipped, res.Stop)
	}
	// Every call still gets a tool result, so the conversation stays valid.
	if got := len(m.seen[1]); got != 1+1+5 {
		t.Errorf("second turn had %d messages, want 7", got)
	}
}
