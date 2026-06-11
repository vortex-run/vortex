package agents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestExecuteLocalTool_SyncApproverExecutes(t *testing.T) {
	// With a synchronous (Telegram-style) approver, the write executes inline.
	c, dir := localCoord(t, func(_ context.Context, req ApprovalRequest) bool {
		return req.Tool == "write_file"
	})
	var steps []string
	_, err := c.ExecuteLocalTool(context.Background(), "s1", "write_file",
		map[string]any{"path": "out.txt", "content": "data"}, func(m string) { steps = append(steps, m) })
	if err != nil {
		t.Fatalf("approved write: %v", err)
	}
	if data, _ := osRead(dir, "out.txt"); data != "data" {
		t.Errorf("file content = %q, want data", data)
	}
	joined := strings.Join(steps, " | ")
	if !strings.Contains(joined, "APPROVAL_REQUIRED") || !strings.Contains(joined, "File created") {
		t.Errorf("write should stream approval + done: %s", joined)
	}
}

func TestExecuteLocalTool_SyncRejectDoesNotWrite(t *testing.T) {
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

func TestExecuteLocalTool_AsyncReturnsImmediately(t *testing.T) {
	// No synchronous approver (the TUI path): ExecuteLocalTool returns at once
	// (it does NOT block), leaving a pending action for ApproveAction.
	c, dir := localCoord(t, nil)
	var steps []string
	res, err := c.ExecuteLocalTool(context.Background(), "s1", "write_file",
		map[string]any{"path": "x.txt", "content": "x"}, func(m string) { steps = append(steps, m) })
	if err != nil || res != nil {
		t.Fatalf("async approval should return (nil,nil), got (%v,%v)", res, err)
	}
	if !c.HasPendingApproval("s1") {
		t.Error("a pending approval should be registered")
	}
	// File must NOT exist yet (not approved).
	if _, rerr := osRead(dir, "x.txt"); rerr == nil {
		t.Error("file must not be written before approval")
	}
	joined := strings.Join(steps, " | ")
	if !strings.Contains(joined, "APPROVAL_REQUIRED") {
		t.Errorf("preview should have streamed: %s", joined)
	}
}

func TestApproveAction_ApproveExecutesAndWrites(t *testing.T) {
	c, dir := localCoord(t, nil)
	_, _ = c.ExecuteLocalTool(context.Background(), "sess", "write_file",
		map[string]any{"path": "ok.txt", "content": "yes"}, func(string) {})
	transcript, matched := c.ApproveAction("sess", true)
	if !matched {
		t.Fatal("ApproveAction should match the pending write")
	}
	if !strings.Contains(transcript, "File created") {
		t.Errorf("approve transcript should confirm the write: %q", transcript)
	}
	if data, _ := osRead(dir, "ok.txt"); data != "yes" {
		t.Errorf("approved file content = %q, want yes", data)
	}
	// Pending entry is consumed.
	if c.HasPendingApproval("sess") {
		t.Error("pending approval should be cleared after resolution")
	}
}

func TestApproveAction_RejectDoesNotWrite(t *testing.T) {
	c, dir := localCoord(t, nil)
	_, _ = c.ExecuteLocalTool(context.Background(), "sess", "write_file",
		map[string]any{"path": "no.txt", "content": "x"}, func(string) {})
	transcript, matched := c.ApproveAction("sess", false)
	if !matched {
		t.Fatal("ApproveAction should match")
	}
	if !strings.Contains(transcript, "rejected") {
		t.Errorf("reject transcript = %q, want a rejection", transcript)
	}
	if _, rerr := osRead(dir, "no.txt"); rerr == nil {
		t.Error("rejected action must not create the file")
	}
}

func TestApproveAction_UnknownSession(t *testing.T) {
	c, _ := localCoord(t, nil)
	if _, matched := c.ApproveAction("ghost", true); matched {
		t.Error("ApproveAction for an unknown session should return matched=false")
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

// codegenGateway returns a fixed code body for any codegen prompt, and records
// whether Complete was called (to prove generation ran BEFORE approval).
type codegenGateway struct {
	called *bool
	body   string
}

func (g codegenGateway) Complete(_ context.Context, _ string, sys string) (string, error) {
	if g.called != nil {
		*g.called = true
	}
	if strings.Contains(strings.ToLower(sys), "code generator") {
		return g.body, nil
	}
	return "GENERAL_QUESTION", nil
}

func TestHandleLocalFile_GeneratesContentBeforeApproval(t *testing.T) {
	dir := t.TempDir()
	reg := NewToolRegistry()
	if err := RegisterLocalTools(reg, LocalFSConfig{Root: dir}); err != nil {
		t.Fatal(err)
	}
	called := false
	var captured ApprovalRequest
	c, err := NewCoordinator(CoordinatorConfig{
		Bus:        NewBus(),
		AIGateway:  codegenGateway{called: &called, body: "def add(a, b):\n    return a + b\n"},
		LocalTools: reg,
		WorkingDir: dir,
		// Capture the approval request to assert the content is present.
		Approval: func(_ context.Context, req ApprovalRequest) bool {
			captured = req
			return false // reject so nothing is written; we only inspect the box
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := c.HandleMessage(context.Background(),
		"create a python calculator save it to "+filepath.Join(dir, "calc.py"), "s1")
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !called {
		t.Fatal("AI codegen should have been called before the approval")
	}
	// The approval preview must contain the generated code, not a blank.
	if !strings.Contains(captured.Preview, "def add") {
		t.Errorf("approval preview should include generated content, got:\n%s", captured.Preview)
	}
	// The transcript should show the generating step.
	if !strings.Contains(out, "Generating code") {
		t.Errorf("transcript should show the generating step:\n%s", out)
	}
}

func TestStripCodeFences(t *testing.T) {
	cases := map[string]string{
		"```python\nprint(1)\n```": "print(1)",
		"plain code\nline 2":       "plain code\nline 2",
		"```\nx = 1\n```":          "x = 1",
	}
	for in, want := range cases {
		if got := stripCodeFences(in); got != want {
			t.Errorf("stripCodeFences(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLanguageForPath(t *testing.T) {
	cases := map[string]string{
		"calc.py": "Python", "main.go": "Go", "app.js": "JavaScript", "x.unknownext": "the appropriate language",
	}
	for path, want := range cases {
		if got := languageForPath(path); got != want {
			t.Errorf("languageForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestToolDoneLine_RunTerminalIncludesOutput(t *testing.T) {
	line := toolDoneLine("run_terminal", map[string]any{
		"stdout": "hello world\n", "stderr": "", "exit_code": 0,
	})
	if !strings.Contains(line, "hello world") {
		t.Errorf("done line should include stdout: %q", line)
	}
	if !strings.Contains(line, "Completed (exit 0)") {
		t.Errorf("done line should include exit code: %q", line)
	}
}

func TestToolDoneLine_RunTerminalIncludesStderr(t *testing.T) {
	line := toolDoneLine("run_terminal", map[string]any{
		"stdout": "", "stderr": "boom\n", "exit_code": 1,
	})
	if !strings.Contains(line, "⚠ boom") {
		t.Errorf("done line should include stderr with warning: %q", line)
	}
	if !strings.Contains(line, "exit 1") {
		t.Errorf("done line should include exit 1: %q", line)
	}
}

func TestApproveAction_RunTerminalReturnsOutput(t *testing.T) {
	dir := t.TempDir()
	reg := NewToolRegistry()
	if err := RegisterLocalTools(reg, LocalFSConfig{Root: dir}); err != nil {
		t.Fatal(err)
	}
	c, _ := NewCoordinator(CoordinatorConfig{Bus: NewBus(), AIGateway: StubAIGateway{}, LocalTools: reg, WorkingDir: dir})
	// Stash a run_terminal action (echo), then approve → output in transcript.
	_, _ = c.ExecuteLocalTool(context.Background(), "sess", "run_terminal",
		map[string]any{"command": "echo vortexout"}, func(string) {})
	transcript, matched := c.ApproveAction("sess", true)
	if !matched {
		t.Fatal("run approval should match")
	}
	if !strings.Contains(transcript, "vortexout") {
		t.Errorf("approve transcript should contain command output: %q", transcript)
	}
}

func TestSession_ClarificationContinuesSameBuild(t *testing.T) {
	var submitted []string
	clarifying := true // simulate forge in JobClarify state after the first build
	c, err := NewCoordinator(CoordinatorConfig{
		Bus:       NewBus(),
		AIGateway: StubAIGateway{IntentReply: string(IntentBuildApp)},
		BuildApp: func(_ context.Context, msg, _ string) (string, error) {
			submitted = append(submitted, msg)
			return "🛠 Build started. Job ID: job-1", nil
		},
		SessionClarifying: func(string) bool { return clarifying },
	})
	if err != nil {
		t.Fatal(err)
	}

	// Turn 1: the build request → dispatched as a new build.
	if _, err := c.HandleMessage(context.Background(), "design a calculator app using flutter", "s1"); err != nil {
		t.Fatal(err)
	}
	// Turn 2: forge is clarifying → the answer must CONTINUE the same build with
	// the original request + the answer, NOT start a fresh unrelated build.
	if _, err := c.HandleMessage(context.Background(), "yes basic calculator with decimals", "s1"); err != nil {
		t.Fatal(err)
	}

	if len(submitted) != 2 {
		t.Fatalf("expected 2 submits, got %d: %v", len(submitted), submitted)
	}
	// The second submission must include BOTH the original request and the answer.
	if !strings.Contains(submitted[1], "calculator app using flutter") ||
		!strings.Contains(submitted[1], "basic calculator with decimals") {
		t.Errorf("clarification submit should combine original + answer, got: %q", submitted[1])
	}
}

func TestSession_NewRequestAfterClarificationResolved(t *testing.T) {
	clarifying := false // job no longer clarifying (e.g. completed)
	var submitted []string
	c, _ := NewCoordinator(CoordinatorConfig{
		Bus:       NewBus(),
		AIGateway: StubAIGateway{IntentReply: string(IntentBuildApp)},
		BuildApp: func(_ context.Context, msg, _ string) (string, error) {
			submitted = append(submitted, msg)
			return "ok", nil
		},
		SessionClarifying: func(string) bool { return clarifying },
	})
	_, _ = c.HandleMessage(context.Background(), "build a go api", "s1")
	// Not clarifying → a second build message is a NEW request, not an answer.
	_, _ = c.HandleMessage(context.Background(), "build a python script", "s1")
	if len(submitted) != 2 {
		t.Fatalf("expected 2 submits, got %v", submitted)
	}
	// The second submit must NOT be combined with the first (fresh request).
	if strings.Contains(submitted[1], "go api") {
		t.Errorf("a new request should not be merged with the old one: %q", submitted[1])
	}
}

func TestSession_PruneIdle(t *testing.T) {
	c, _ := NewCoordinator(CoordinatorConfig{Bus: NewBus(), AIGateway: StubAIGateway{}})
	c.mu.Lock()
	c.sessions["old"] = &SessionState{LastActivity: time.Now().Add(-2 * sessionTTL)}
	c.sessions["fresh"] = &SessionState{LastActivity: time.Now()}
	c.mu.Unlock()
	c.pruneIdleSessions()
	c.mu.Lock()
	_, oldOK := c.sessions["old"]
	_, freshOK := c.sessions["fresh"]
	c.mu.Unlock()
	if oldOK {
		t.Error("idle session should be pruned")
	}
	if !freshOK {
		t.Error("fresh session should be kept")
	}
}

func TestSession_ClearSession(t *testing.T) {
	c, _ := NewCoordinator(CoordinatorConfig{Bus: NewBus(), AIGateway: StubAIGateway{}})
	c.mu.Lock()
	c.sessions["s1"] = &SessionState{LastActivity: time.Now()}
	c.mu.Unlock()
	c.ClearSession("s1")
	if c.isAwaitingClarification("s1") {
		t.Error("cleared session should not be awaiting")
	}
}

func TestDescribeProject_DetectsTypes(t *testing.T) {
	cases := []struct {
		file, content, wantType string
	}{
		{"go.mod", "module x\n\ngo 1.26\n", "Go"},
		{"package.json", "{\"name\":\"x\"}", "Node"},
		{"requirements.txt", "flask\n", "Python"},
		{"pubspec.yaml", "name: x\n", "Flutter"},
		{"pom.xml", "<project/>", "Java"},
		{"Cargo.toml", "[package]\n", "Rust"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, c.file), []byte(c.content), 0o600); err != nil {
			t.Fatal(err)
		}
		got := describeProject(dir)
		if !strings.Contains(got, c.wantType) {
			t.Errorf("describeProject with %s = %q, want it to mention %s", c.file, got, c.wantType)
		}
	}
}

func TestDescribeProject_CSharp(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "app.csproj"), []byte("<Project/>"), 0o600)
	if got := describeProject(dir); !strings.Contains(got, "C#") {
		t.Errorf("describeProject = %q, want C#", got)
	}
}

func TestDescribeProject_Unknown(t *testing.T) {
	if got := describeProject(t.TempDir()); !strings.Contains(got, "no recognised project type") {
		t.Errorf("empty dir = %q, want unknown", got)
	}
}

func TestAgentSystemPrompt_IncludesContext(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module svc\n"), 0o600)
	c, _ := NewCoordinator(CoordinatorConfig{Bus: NewBus(), AIGateway: StubAIGateway{}, WorkingDir: dir})
	prompt := c.agentSystemPrompt()
	if !strings.Contains(prompt, "Go project") || !strings.Contains(prompt, "read_file first") {
		t.Errorf("system prompt should include project context + tool guidance:\n%s", prompt)
	}
}

// TestClarification_AnswerContinuesSameJob verifies the loop fix: when a build
// for a session is still in flight (pending) — even before it reaches
// needs_clarification — the next message is treated as an ANSWER that continues
// the SAME session, not a new build request.
func TestClarification_AnswerContinuesSameJob(t *testing.T) {
	var submissions []string
	// Simulate forge: the first build is "pending" (async, not yet clarifying)
	// when the answer arrives — the exact window that used to start a new job.
	pending := false
	c, err := NewCoordinator(CoordinatorConfig{
		Bus:       NewBus(),
		AIGateway: StubAIGateway{IntentReply: string(IntentBuildApp)},
		BuildApp: func(_ context.Context, msg, _ string) (string, error) {
			submissions = append(submissions, msg)
			pending = true // a build is now in flight for this session
			return "🛠 Build started. Job ID: job-1", nil
		},
		// No clarifying yet (job hasn't reached needs_clarification), but the
		// build IS pending — the answer must still continue the session.
		SessionClarifying: func(string) bool { return false },
		SessionPending:    func(string) bool { return pending },
	})
	if err != nil {
		t.Fatal(err)
	}

	// Turn 1: the build request → dispatched.
	if _, err := c.HandleMessage(context.Background(), "design a flutter calculator", "sess-A"); err != nil {
		t.Fatal(err)
	}
	// Turn 2: the answer arrives while the build is pending (not yet clarifying).
	if _, err := c.HandleMessage(context.Background(), "no a basic calculator", "sess-A"); err != nil {
		t.Fatal(err)
	}

	if len(submissions) != 2 {
		t.Fatalf("expected 2 submissions, got %d: %v", len(submissions), submissions)
	}
	// The second submission must CONTINUE the same build (original + answer),
	// NOT be a fresh unrelated request.
	if !strings.Contains(submissions[1], "flutter calculator") ||
		!strings.Contains(submissions[1], "basic calculator") {
		t.Errorf("answer should continue the same job (original + answer), got: %q", submissions[1])
	}
}

func TestClarification_NewRequestWhenBuildDone(t *testing.T) {
	var submissions []string
	pending := false // build finished (terminal)
	c, _ := NewCoordinator(CoordinatorConfig{
		Bus:       NewBus(),
		AIGateway: StubAIGateway{IntentReply: string(IntentBuildApp)},
		BuildApp: func(_ context.Context, msg, _ string) (string, error) {
			submissions = append(submissions, msg)
			return "ok", nil
		},
		SessionClarifying: func(string) bool { return false },
		SessionPending:    func(string) bool { return pending },
	})
	_, _ = c.HandleMessage(context.Background(), "build a go api", "sess-B")
	// Build is done (pending=false) → a second build message is a NEW request.
	_, _ = c.HandleMessage(context.Background(), "build a python script", "sess-B")
	if len(submissions) != 2 {
		t.Fatalf("expected 2 submissions, got %v", submissions)
	}
	if strings.Contains(submissions[1], "go api") {
		t.Errorf("a new request after the build finished should not merge: %q", submissions[1])
	}
}

func TestRuleClassify_Research(t *testing.T) {
	for _, msg := range []string{
		"/research golang frameworks",
		"research the best databases",
		"look up rust web servers",
		"search for kubernetes operators",
		"tell me about vector databases",
		"summarize the latest go release",
	} {
		if got := ruleClassify(msg); got != IntentResearch {
			t.Errorf("ruleClassify(%q) = %q, want RESEARCH", msg, got)
		}
	}
}

func TestExtractResearchQuery(t *testing.T) {
	cases := map[string]string{
		"/research golang frameworks": "golang frameworks",
		"research the best databases": "the best databases",
		"tell me about vector dbs":    "vector dbs",
		"summarize the go release":    "the go release",
	}
	for in, want := range cases {
		if got := extractResearchQuery(in); got != want {
			t.Errorf("extractResearchQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHandleMessage_ResearchRoutesToHandler(t *testing.T) {
	var gotQuery string
	c, _ := NewCoordinator(CoordinatorConfig{
		Bus:       NewBus(),
		AIGateway: StubAIGateway{},
		Research: func(_ context.Context, query string, _ func(string)) (string, error) {
			gotQuery = query
			return "📊 Research complete: " + query, nil
		},
	})
	out, err := c.HandleMessage(context.Background(), "research go web frameworks", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "go web frameworks" {
		t.Errorf("research handler got query %q", gotQuery)
	}
	if !strings.Contains(out, "Research complete") {
		t.Errorf("reply = %q", out)
	}
}

func TestHandleResearch_StubWhenNotWired(t *testing.T) {
	c, _ := NewCoordinator(CoordinatorConfig{Bus: NewBus(), AIGateway: StubAIGateway{}})
	out, err := c.HandleMessage(context.Background(), "/research something", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "RESEARCH") {
		t.Errorf("unwired research should stub, got %q", out)
	}
}

func TestRuleClassify_DevOps(t *testing.T) {
	for _, msg := range []string{
		"server status", "docker ps", "list containers", "restart service nginx",
		"install package htop", "add nginx site api.example.com", "enable ssl x.com",
		"disk space", "ssh into prod",
	} {
		if got := ruleClassify(msg); got != IntentDevOpsCheck {
			t.Errorf("ruleClassify(%q) = %q, want DEVOPS_CHECK", msg, got)
		}
	}
}

func TestHandleMessage_DevOpsRoutesToHandler(t *testing.T) {
	var gotMsg string
	c, _ := NewCoordinator(CoordinatorConfig{
		Bus:       NewBus(),
		AIGateway: StubAIGateway{},
		DevOps: func(_ context.Context, m string, _ func(string)) (string, error) {
			gotMsg = m
			return "Host: prod (ubuntu/amd64)", nil
		},
	})
	out, err := c.HandleMessage(context.Background(), "server status", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if gotMsg != "server status" {
		t.Errorf("devops handler got %q", gotMsg)
	}
	if !strings.Contains(out, "ubuntu") {
		t.Errorf("reply = %q", out)
	}
}

func TestHandleDevOps_StubWhenNotWired(t *testing.T) {
	c, _ := NewCoordinator(CoordinatorConfig{Bus: NewBus(), AIGateway: StubAIGateway{}})
	out, err := c.HandleMessage(context.Background(), "docker ps", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "DEVOPS_CHECK") {
		t.Errorf("unwired devops should stub, got %q", out)
	}
}

func TestRuleClassify_Pipeline(t *testing.T) {
	for _, msg := range []string{
		"analyze sales.csv", "chart the revenue", "plot monthly users",
		"visualize the data", "graph the results", "group by region",
	} {
		if got := ruleClassify(msg); got != IntentDataPipeline {
			t.Errorf("ruleClassify(%q) = %q, want DATA_PIPELINE", msg, got)
		}
	}
}

func TestHandleMessage_PipelineRoutesToHandler(t *testing.T) {
	var gotMsg string
	c, _ := NewCoordinator(CoordinatorConfig{
		Bus:       NewBus(),
		AIGateway: StubAIGateway{},
		Pipeline: func(_ context.Context, m string, _ func(string)) (string, error) {
			gotMsg = m
			return "📊 Produced 3 result rows", nil
		},
	})
	out, err := c.HandleMessage(context.Background(), "analyze the sales data", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if gotMsg != "analyze the sales data" {
		t.Errorf("pipeline handler got %q", gotMsg)
	}
	if !strings.Contains(out, "result rows") {
		t.Errorf("reply = %q", out)
	}
}

func TestHandlePipeline_StubWhenNotWired(t *testing.T) {
	c, _ := NewCoordinator(CoordinatorConfig{Bus: NewBus(), AIGateway: StubAIGateway{}})
	out, err := c.HandleMessage(context.Background(), "chart the revenue", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "DATA_PIPELINE") {
		t.Errorf("unwired pipeline should stub, got %q", out)
	}
}

func TestRuleClassify_Orchestrate(t *testing.T) {
	for _, msg := range []string{
		"/orchestrate research X then summarize",
		"orchestrate: build and deploy the app",
	} {
		if got := ruleClassify(msg); got != IntentOrchestrate {
			t.Errorf("ruleClassify(%q) = %q, want ORCHESTRATE", msg, got)
		}
	}
}

func TestHandleMessage_OrchestrateRoutesToHandler(t *testing.T) {
	var gotGoal string
	c, _ := NewCoordinator(CoordinatorConfig{
		Bus:       NewBus(),
		AIGateway: StubAIGateway{},
		Orchestrate: func(_ context.Context, goal string, _ func(string)) (string, error) {
			gotGoal = goal
			return "2 completed, 0 failed", nil
		},
	})
	out, err := c.HandleMessage(context.Background(), "/orchestrate investigate X and report", "s1")
	if err != nil {
		t.Fatal(err)
	}
	// The /orchestrate prefix is stripped before dispatch.
	if gotGoal != "investigate X and report" {
		t.Errorf("orchestrate handler got goal %q", gotGoal)
	}
	if !strings.Contains(out, "completed") {
		t.Errorf("reply = %q", out)
	}
}

func TestHandleOrchestrate_StubWhenNotWired(t *testing.T) {
	c, _ := NewCoordinator(CoordinatorConfig{Bus: NewBus(), AIGateway: StubAIGateway{}})
	out, err := c.HandleMessage(context.Background(), "/orchestrate do stuff", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ORCHESTRATE") {
		t.Errorf("unwired orchestrate should stub, got %q", out)
	}
}

func TestHandleOrchestrate_EmptyGoalAsks(t *testing.T) {
	c, _ := NewCoordinator(CoordinatorConfig{
		Bus: NewBus(), AIGateway: StubAIGateway{},
		Orchestrate: func(context.Context, string, func(string)) (string, error) { return "ran", nil },
	})
	out, err := c.HandleMessage(context.Background(), "/orchestrate", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(out), "what goal") {
		t.Errorf("empty goal should ask for one, got %q", out)
	}
}

func TestEvictOldestSessions_EnforcesCap(t *testing.T) {
	m := map[string]*SessionState{}
	base := time.Now()
	for i := 0; i < 100; i++ {
		m[fmtID(i)] = &SessionState{LastActivity: base.Add(time.Duration(i) * time.Second)}
	}
	evictOldestSessions(m, 10)
	if len(m) != 10 {
		t.Fatalf("after eviction len = %d, want 10", len(m))
	}
	// The 10 newest (ids 90..99) should remain.
	for i := 90; i < 100; i++ {
		if _, ok := m[fmtID(i)]; !ok {
			t.Errorf("newest session %d should survive eviction", i)
		}
	}
}

func TestEvictOldestMemories_EnforcesCap(t *testing.T) {
	m := map[string]*Memory{}
	base := time.Now()
	for i := 0; i < 50; i++ {
		m[fmtID(i)] = &Memory{UpdatedAt: base.Add(time.Duration(i) * time.Second)}
	}
	evictOldestMemories(m, 5)
	if len(m) != 5 {
		t.Fatalf("after eviction len = %d, want 5", len(m))
	}
}

func TestEvictOldest_NoOpUnderCap(t *testing.T) {
	m := map[string]*SessionState{"a": {LastActivity: time.Now()}}
	evictOldestSessions(m, 10)
	if len(m) != 1 {
		t.Errorf("under-cap map should be unchanged, len = %d", len(m))
	}
}

func fmtID(i int) string { return fmt.Sprintf("s%d", i) }

// skillCapturingGateway records every system prompt it receives and answers
// intent, skill-extraction, and direct-answer prompts appropriately.
type skillCapturingGateway struct {
	mu         sync.Mutex
	sysPrompts []string
}

func (g *skillCapturingGateway) Complete(_ context.Context, _, systemPrompt string) (string, error) {
	g.mu.Lock()
	g.sysPrompts = append(g.sysPrompts, systemPrompt)
	g.mu.Unlock()
	low := strings.ToLower(systemPrompt)
	if strings.Contains(low, "classify") {
		return string(IntentGeneralQuestion), nil
	}
	if strings.Contains(low, "reusable skill") {
		return skillDocJSON, nil
	}
	return "done", nil
}

func (g *skillCapturingGateway) prompts() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.sysPrompts...)
}

func saveSkillWithRecord(t *testing.T, store *SkillStore, name string, trigger []string, rate float64, used int) *Skill {
	t.Helper()
	sk := &Skill{
		Name: name, Description: "proven procedure for " + name,
		Trigger:     trigger,
		Steps:       []SkillStep{{Description: "step one"}, {Description: "step two"}},
		SuccessRate: rate, UsedCount: used,
	}
	if err := store.Save(sk); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return sk
}

func TestHandleMessage_SkillIncludedInPrompt(t *testing.T) {
	gw := &skillCapturingGateway{}
	c := newTestCoordinator(t, gw)
	store := newTestSkillStore(t)
	c.SetSkillStore(store)
	sk := saveSkillWithRecord(t, store, "Create FastAPI app", []string{"fastapi"}, 1.0, 5)

	if _, err := c.HandleMessage(context.Background(), "how do I set up a fastapi project?", "s1"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	var found bool
	for _, p := range gw.prompts() {
		if strings.Contains(p, "proven skill") && strings.Contains(p, sk.Name) {
			found = true
		}
	}
	if !found {
		t.Error("skill block not included in any system prompt")
	}
	// The skill-guided answer succeeded, so its record was updated.
	skills, _ := store.List()
	if skills[0].UsedCount != 6 {
		t.Errorf("UsedCount = %d, want 6 (MarkUsed after answer)", skills[0].UsedCount)
	}
}

func TestHandleMessage_LowSuccessRateSkillIgnored(t *testing.T) {
	gw := &skillCapturingGateway{}
	c := newTestCoordinator(t, gw)
	store := newTestSkillStore(t)
	c.SetSkillStore(store)
	saveSkillWithRecord(t, store, "Flaky FastAPI procedure", []string{"fastapi"}, 0.3, 10)

	if _, err := c.HandleMessage(context.Background(), "how do I set up a fastapi project?", "s1"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	for _, p := range gw.prompts() {
		if strings.Contains(p, "proven skill") {
			t.Error("low success rate skill was included in prompt")
		}
	}
	skills, _ := store.List()
	if skills[0].UsedCount != 10 {
		t.Errorf("UsedCount = %d, want 10 (unused skill must not be marked)", skills[0].UsedCount)
	}
}

func TestHandleMessage_HighSuccessRateSkillPreferred(t *testing.T) {
	gw := &skillCapturingGateway{}
	c := newTestCoordinator(t, gw)
	store := newTestSkillStore(t)
	c.SetSkillStore(store)
	good := saveSkillWithRecord(t, store, "Reliable FastAPI procedure", []string{"fastapi"}, 0.9, 10)
	bad := saveSkillWithRecord(t, store, "Flaky FastAPI procedure", []string{"fastapi"}, 0.4, 10)

	if _, err := c.HandleMessage(context.Background(), "how do I set up a fastapi project?", "s1"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	var sawGood, sawBad bool
	for _, p := range gw.prompts() {
		if strings.Contains(p, good.Name) {
			sawGood = true
		}
		if strings.Contains(p, bad.Name) {
			sawBad = true
		}
	}
	if !sawGood || sawBad {
		t.Errorf("prompt skill selection: good=%v bad=%v, want good only", sawGood, sawBad)
	}
}

func TestHandleMessage_OrchestrateTriggersMaybeLearn(t *testing.T) {
	gw := &skillCapturingGateway{}
	c := newTestCoordinator(t, gw)
	store := newTestSkillStore(t)
	c.SetSkillStore(store)

	c.cfg.Orchestrate = func(_ context.Context, _ string, progressFn func(string)) (string, error) {
		progressFn("planned tasks")
		progressFn("built backend")
		progressFn("ran tests")
		return "all tasks complete", nil
	}

	if _, err := c.HandleMessage(context.Background(), "/orchestrate build a fastapi app with tests", "s1"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	skills, err := store.List()
	if err != nil || len(skills) != 1 {
		t.Fatalf("List = %d skills (%v), want 1 learned from orchestration", len(skills), err)
	}
	if skills[0].Name != "Create FastAPI app" {
		t.Errorf("learned skill = %q", skills[0].Name)
	}
}

func TestHandleMessage_FailedOrchestrationNotLearned(t *testing.T) {
	gw := &skillCapturingGateway{}
	c := newTestCoordinator(t, gw)
	store := newTestSkillStore(t)
	c.SetSkillStore(store)

	c.cfg.Orchestrate = func(_ context.Context, _ string, progressFn func(string)) (string, error) {
		progressFn("planned tasks")
		progressFn("built backend")
		progressFn("tests failed")
		return "", fmt.Errorf("orchestration failed")
	}

	_, _ = c.HandleMessage(context.Background(), "/orchestrate build a fastapi app with tests", "s1")
	if skills, _ := store.List(); len(skills) != 0 {
		t.Errorf("failed orchestration learned %d skills, want 0", len(skills))
	}
}

func TestSetSkillStoreNilDisables(t *testing.T) {
	c := newTestCoordinator(t, StubAIGateway{})
	store := newTestSkillStore(t)
	c.SetSkillStore(store)
	c.SetSkillStore(nil)
	if sk := c.findSkill("anything"); sk != nil {
		t.Errorf("findSkill after disable = %v, want nil", sk)
	}
	c.maybeLearn(context.Background(), "task", []string{"a", "b", "c"}, "done", true) // must not panic
}
