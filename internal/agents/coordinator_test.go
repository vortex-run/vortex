package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestCoordinator(t *testing.T, gw AIGateway) *Coordinator {
	t.Helper()
	reg := NewToolRegistry()
	_ = reg.Register(ReadFileTool{SandboxDir: t.TempDir()})
	tools := NewSandboxedRegistry(reg, t.TempDir(), nil, nil)
	c, err := NewCoordinator(CoordinatorConfig{
		Bus:       NewBus(),
		Tools:     tools,
		AIGateway: gw,
		MaxAgents: 2,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	return c
}

func TestNewCoordinator_RequiresBusAndGateway(t *testing.T) {
	if _, err := NewCoordinator(CoordinatorConfig{AIGateway: StubAIGateway{}}); err == nil {
		t.Error("expected error without bus")
	}
	if _, err := NewCoordinator(CoordinatorConfig{Bus: NewBus()}); err == nil {
		t.Error("expected error without gateway")
	}
}

func TestHandleMessage_GeneralQuestionAnswered(t *testing.T) {
	c := newTestCoordinator(t, StubAIGateway{
		IntentReply: string(IntentGeneralQuestion),
		AnswerReply: "42",
	})
	out, err := c.HandleMessage(context.Background(), "what is 6 times 7?", "s1")
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if out != "42" {
		t.Errorf("answer = %q, want 42", out)
	}
}

func TestHandleMessage_BuildAppReturnsStub(t *testing.T) {
	c := newTestCoordinator(t, StubAIGateway{IntentReply: string(IntentBuildApp)})
	out, err := c.HandleMessage(context.Background(), "build me an app", "s1")
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !strings.Contains(out, "BUILD_APP") || !strings.Contains(out, "not yet implemented") {
		t.Errorf("build response = %q, want stub for BUILD_APP", out)
	}
}

func TestHandleMessage_UnknownAsksToClarify(t *testing.T) {
	c := newTestCoordinator(t, StubAIGateway{IntentReply: "WAT"})
	out, err := c.HandleMessage(context.Background(), "fhqwhgads", "s1")
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "clarify") {
		t.Errorf("unknown response = %q, want a clarifying question", out)
	}
}

func TestHandleMessage_EmptyErrors(t *testing.T) {
	c := newTestCoordinator(t, StubAIGateway{})
	if _, err := c.HandleMessage(context.Background(), "   ", "s1"); err == nil {
		t.Error("expected error on empty message")
	}
}

func TestSpawnAgent_CreatesWithTools(t *testing.T) {
	c := newTestCoordinator(t, StubAIGateway{})
	ag, err := c.SpawnAgent(context.Background(),
		AgentConfig{Name: "w1", Type: TypeTask}, []string{"read_file"})
	if err != nil {
		t.Fatalf("SpawnAgent: %v", err)
	}
	if ag.Name() != "w1" {
		t.Errorf("agent name = %q, want w1", ag.Name())
	}
	if sa, ok := ag.(*subAgent); !ok || len(sa.Tools()) != 1 || sa.Tools()[0] != "read_file" {
		t.Errorf("sub-agent tools wrong: %+v", ag)
	}
}

func TestSpawnAgent_UnknownToolRejected(t *testing.T) {
	c := newTestCoordinator(t, StubAIGateway{})
	if _, err := c.SpawnAgent(context.Background(),
		AgentConfig{Name: "w", Type: TypeTask}, []string{"nope"}); err == nil {
		t.Error("expected error spawning with unknown tool")
	}
}

func TestSpawnAgent_RespectsMaxAgents(t *testing.T) {
	c := newTestCoordinator(t, StubAIGateway{}) // MaxAgents = 2
	for i, name := range []string{"a", "b"} {
		if _, err := c.SpawnAgent(context.Background(),
			AgentConfig{Name: name, Type: TypeTask}, nil); err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
	}
	if _, err := c.SpawnAgent(context.Background(),
		AgentConfig{Name: "c", Type: TypeTask}, nil); err == nil {
		t.Error("third spawn should exceed MaxAgents")
	}
}

func TestActiveAgents_ReturnsSpawned(t *testing.T) {
	c := newTestCoordinator(t, StubAIGateway{})
	_, _ = c.SpawnAgent(context.Background(), AgentConfig{Name: "x", Type: TypeTask}, nil)
	got := c.ActiveAgents()
	if len(got) != 1 || got[0] != "x" {
		t.Errorf("ActiveAgents = %v, want [x]", got)
	}
}

func TestReap_RemovesCompletedAgent(t *testing.T) {
	c := newTestCoordinator(t, StubAIGateway{})
	ag, _ := c.SpawnAgent(context.Background(), AgentConfig{Name: "done", Type: TypeTask}, nil)
	// Drive the agent to Complete.
	_ = ag.Start(context.Background())
	if ag.State() != StateComplete {
		t.Fatalf("agent state = %q, want Complete", ag.State())
	}
	c.Reap("done")
	if len(c.ActiveAgents()) != 0 {
		t.Errorf("ActiveAgents after reap = %v, want empty", c.ActiveAgents())
	}
}

func TestCoordinator_RunToolApprovalApproved(t *testing.T) {
	reg := NewToolRegistry()
	dir := t.TempDir()
	_ = reg.Register(NewRunCommandTool(dir, []string{"go"})) // RequireApproval=true
	tools := NewSandboxedRegistry(reg, dir, []string{"go"}, nil)

	var asked bool
	c, err := NewCoordinator(CoordinatorConfig{
		Bus:       NewBus(),
		Tools:     tools,
		AIGateway: StubAIGateway{},
		Approval: func(_ context.Context, req ApprovalRequest) bool {
			asked = true
			return req.Command == "go" // approve go commands
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.RunTool(context.Background(), "run_command",
		map[string]any{"command": "go", "args": []string{"version"}})
	if err != nil {
		t.Fatalf("RunTool approved: %v", err)
	}
	if !asked {
		t.Error("approval function should have been consulted")
	}
	if m, ok := res.(map[string]any); !ok || m["exit_code"].(int) != 0 {
		t.Errorf("expected successful run after approval, got %v", res)
	}
}

func TestCoordinator_RunToolApprovalRejected(t *testing.T) {
	reg := NewToolRegistry()
	dir := t.TempDir()
	_ = reg.Register(NewRunCommandTool(dir, []string{"go"}))
	tools := NewSandboxedRegistry(reg, dir, []string{"go"}, nil)

	c, _ := NewCoordinator(CoordinatorConfig{
		Bus: NewBus(), Tools: tools, AIGateway: StubAIGateway{},
		Approval: func(context.Context, ApprovalRequest) bool { return false },
	})
	if _, err := c.RunTool(context.Background(), "run_command",
		map[string]any{"command": "go", "args": []string{"version"}}); err == nil {
		t.Error("RunTool should error when approval is rejected")
	}
}

func TestCoordinator_RunToolNilApprovalDenies(t *testing.T) {
	reg := NewToolRegistry()
	dir := t.TempDir()
	_ = reg.Register(NewRunCommandTool(dir, []string{"go"}))
	tools := NewSandboxedRegistry(reg, dir, []string{"go"}, nil)

	c, _ := NewCoordinator(CoordinatorConfig{
		Bus: NewBus(), Tools: tools, AIGateway: StubAIGateway{}, // no Approval
	})
	if _, err := c.RunTool(context.Background(), "run_command",
		map[string]any{"command": "go", "args": []string{"version"}}); err == nil {
		t.Error("RunTool should deny a gated command when no approver is configured")
	}
}

func TestCoordinator_RunToolNoApprovalNeeded(t *testing.T) {
	reg := NewToolRegistry()
	dir := t.TempDir()
	_ = reg.Register(ReadFileTool{SandboxDir: dir})
	tools := NewSandboxedRegistry(reg, dir, nil, nil)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, _ := NewCoordinator(CoordinatorConfig{
		Bus: NewBus(), Tools: tools, AIGateway: StubAIGateway{},
	})
	res, err := c.RunTool(context.Background(), "read_file", map[string]any{"path": "f.txt"})
	if err != nil {
		t.Fatalf("read_file (no approval needed): %v", err)
	}
	if res.(map[string]any)["content"].(string) != "hi" {
		t.Errorf("unexpected content: %v", res)
	}
}
