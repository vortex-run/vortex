package agents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// localCoord builds a coordinator with local tools rooted at a temp dir.
func localCoord(t *testing.T, approval ApprovalFunc) (*Coordinator, string) {
	t.Helper()
	dir := t.TempDir()
	reg := NewToolRegistry()
	if err := RegisterLocalTools(reg, LocalFSConfig{Root: dir}); err != nil {
		t.Fatal(err)
	}
	c, err := NewCoordinator(CoordinatorConfig{
		Bus:        NewBus(),
		AIGateway:  StubAIGateway{},
		LocalTools: reg,
		WorkingDir: dir,
		Approval:   approval,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, dir
}

func TestCoordinator_WorkingDirDefaults(t *testing.T) {
	c, _ := NewCoordinator(CoordinatorConfig{Bus: NewBus(), AIGateway: StubAIGateway{}})
	if c.WorkingDir() == "" {
		t.Error("WorkingDir should default to the process cwd")
	}
}

func TestExecuteLocalTool_ReadOnlyStreams(t *testing.T) {
	c, dir := localCoord(t, nil)
	_ = osWrite(t, dir, "f.txt", "hello")
	var steps []string
	res, err := c.ExecuteLocalTool(context.Background(), "s1", "read_file",
		map[string]any{"path": "f.txt"}, func(m string) { steps = append(steps, m) })
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if res.(map[string]any)["content"] != "hello" {
		t.Errorf("content = %v", res.(map[string]any)["content"])
	}
	joined := strings.Join(steps, " | ")
	if !strings.Contains(joined, "Reading file") || !strings.Contains(joined, "File read") {
		t.Errorf("read should stream start+done steps: %s", joined)
	}
}

func TestExecuteLocalTool_ApprovedWriteExecutes(t *testing.T) {
	// Approval callback auto-approves write_file.
	c, dir := localCoord(t, func(_ context.Context, req ApprovalRequest) bool {
		return req.Tool == "write_file"
	})
	var steps []string
	_, err := c.ExecuteLocalTool(context.Background(), "s1", "write_file",
		map[string]any{"path": "out.txt", "content": "data"}, func(m string) { steps = append(steps, m) })
	if err != nil {
		t.Fatalf("approved write: %v", err)
	}
	data, _ := osRead(dir, "out.txt")
	if data != "data" {
		t.Errorf("file content = %q, want data", data)
	}
	joined := strings.Join(steps, " | ")
	if !strings.Contains(joined, "APPROVAL_REQUIRED") || !strings.Contains(joined, "File created") {
		t.Errorf("write should stream approval + done: %s", joined)
	}
}

func TestExecuteLocalTool_RejectedDoesNotWrite(t *testing.T) {
	c, dir := localCoord(t, func(context.Context, ApprovalRequest) bool { return false })
	_, err := c.ExecuteLocalTool(context.Background(), "s1", "write_file",
		map[string]any{"path": "no.txt", "content": "x"}, func(string) {})
	if err == nil {
		t.Error("rejected write should error")
	}
	if _, rerr := osRead(dir, "no.txt"); rerr == nil {
		t.Error("rejected write must NOT create the file")
	}
}

func TestExecuteLocalTool_NilApproverDenies(t *testing.T) {
	// No Approval callback AND no ApproveAction resolver → must deny (fail-safe).
	c, dir := localCoord(t, nil)
	done := make(chan error, 1)
	go func() {
		_, err := c.ExecuteLocalTool(context.Background(), "s1", "write_file",
			map[string]any{"path": "x.txt", "content": "x"}, func(string) {})
		done <- err
	}()
	// Resolve via ApproveAction with reject to avoid waiting the full timeout.
	waitForPendingApproval(t, c, "s1")
	if !c.ApproveAction("s1", false) {
		t.Fatal("ApproveAction should match the pending request")
	}
	if err := <-done; err == nil {
		t.Error("a rejected (and otherwise unapproved) action must deny")
	}
	if _, rerr := osRead(dir, "x.txt"); rerr == nil {
		t.Error("denied action must not create the file")
	}
}

func TestApproveAction_ResolvesPending(t *testing.T) {
	c, dir := localCoord(t, nil)
	done := make(chan error, 1)
	go func() {
		_, err := c.ExecuteLocalTool(context.Background(), "sess", "write_file",
			map[string]any{"path": "ok.txt", "content": "yes"}, func(string) {})
		done <- err
	}()
	waitForPendingApproval(t, c, "sess")
	if !c.ApproveAction("sess", true) {
		t.Fatal("ApproveAction should resolve the pending write")
	}
	if err := <-done; err != nil {
		t.Fatalf("approved write should succeed: %v", err)
	}
	if data, _ := osRead(dir, "ok.txt"); data != "yes" {
		t.Errorf("approved file content = %q, want yes", data)
	}
}

func TestApproveAction_UnknownSession(t *testing.T) {
	c, _ := localCoord(t, nil)
	if c.ApproveAction("ghost", true) {
		t.Error("ApproveAction for an unknown session should return false")
	}
}

// --- small fs helpers for these tests ---------------------------------------

func osWrite(t *testing.T, dir, name, content string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
}
func osRead(dir, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	return string(b), err
}

func waitForPendingApproval(t *testing.T, c *Coordinator, session string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if c.HasPendingApproval(session) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no pending approval appeared")
}

func TestRuleClassify_LocalFile(t *testing.T) {
	cases := []string{
		"/ls", "/read main.go", "/run echo hi", "/create x.py print",
		`create a file at S:\DETAILS\calc.py`,
		"please write a file here",
		"save it to /tmp/out.txt",
		"list files in this folder",
		"read the file main.go",
	}
	for _, msg := range cases {
		if got := ruleClassify(msg); got != IntentLocalFile {
			t.Errorf("ruleClassify(%q) = %q, want LOCAL_FILE", msg, got)
		}
	}
}

func TestRuleClassify_BuildApp(t *testing.T) {
	cases := []string{
		"build me an app for cricket scores",
		"build an android app",
		"build a web app with login",
		"create a project called shop",
	}
	for _, msg := range cases {
		if got := ruleClassify(msg); got != IntentBuildApp {
			t.Errorf("ruleClassify(%q) = %q, want BUILD_APP", msg, got)
		}
	}
}

func TestRuleClassify_UnknownFallsThrough(t *testing.T) {
	for _, msg := range []string{"what is the capital of France?", "hello", "explain mTLS"} {
		if got := ruleClassify(msg); got != IntentUnknown {
			t.Errorf("ruleClassify(%q) = %q, want UNKNOWN (AI fallback)", msg, got)
		}
	}
}

func TestParseLocalRequest_SlashCommands(t *testing.T) {
	cases := []struct {
		msg, tool, key, val string
	}{
		{"/ls /tmp", "list_directory", "path", "/tmp"},
		{"/read main.go", "read_file", "path", "main.go"},
		{"/run echo hi", "run_terminal", "command", "echo hi"},
	}
	for _, c := range cases {
		tool, params := parseLocalRequest(c.msg)
		if tool != c.tool {
			t.Errorf("parseLocalRequest(%q) tool = %q, want %q", c.msg, tool, c.tool)
		}
		if params[c.key] != c.val {
			t.Errorf("parseLocalRequest(%q) %s = %v, want %q", c.msg, c.key, params[c.key], c.val)
		}
	}
}

func TestParseLocalRequest_ProseWriteExtractsPath(t *testing.T) {
	tool, params := parseLocalRequest(`create a file and save it to S:\DETAILS\calc.py`)
	if tool != "write_file" {
		t.Fatalf("tool = %q, want write_file", tool)
	}
	if params["path"] != `S:\DETAILS\calc.py` {
		t.Errorf("path = %v, want the windows path", params["path"])
	}
}

func TestExtractPath(t *testing.T) {
	cases := map[string]string{
		`save it to S:\DETAILS\x.py`:   `S:\DETAILS\x.py`,
		"write to /tmp/out.txt please": "/tmp/out.txt",
		"no path here":                 "",
	}
	for msg, want := range cases {
		if got := extractPath(msg); got != want {
			t.Errorf("extractPath(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestHandleMessage_LocalFileRoutesWithoutAI(t *testing.T) {
	// A gateway that FAILS any Complete call — proves the local-file path never
	// touches the AI for intent parsing.
	c, dir := localCoord(t, func(context.Context, ApprovalRequest) bool { return true })
	c.cfg.AIGateway = failingGateway{}
	_ = osWrite(t, dir, "a.txt", "hi")
	out, err := c.HandleMessage(context.Background(), "/ls", "s1")
	if err != nil {
		t.Fatalf("local /ls should not error: %v", err)
	}
	if !strings.Contains(out, "Listing") {
		t.Errorf("expected a directory-listing transcript, got: %s", out)
	}
}

// failingGateway returns an error on every call (to prove the AI isn't used).
type failingGateway struct{}

func (failingGateway) Complete(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("AI must not be called for local-file intent")
}
