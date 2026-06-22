package specialists

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vortex-run/vortex/internal/a2a"
)

// fakeTools is a configurable in-memory tool executor for specialist tests.
type fakeTools struct {
	mu      sync.Mutex
	written map[string]string // path → content (write_file)
	reads   map[string]string // path → content served by read_file
	dirList string
	termOut string
	termErr error
	calls   []string
}

func newFakeTools() *fakeTools {
	return &fakeTools{written: map[string]string{}, reads: map[string]string{}}
}

func (f *fakeTools) run(_ context.Context, name string, params map[string]any) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	switch name {
	case "write_file":
		path, _ := params["path"].(string)
		content, _ := params["content"].(string)
		f.written[path] = content
		return map[string]any{"path": path, "bytes_written": len(content)}, nil
	case "read_file":
		path, _ := params["path"].(string)
		return f.reads[path], nil
	case "list_directory":
		return f.dirList, nil
	case "run_terminal":
		return f.termOut, f.termErr
	default:
		return "", nil
	}
}

func (f *fakeTools) calledWith(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == name {
			return true
		}
	}
	return false
}

func codeAgentWith(t *testing.T, gw AIGateway, tools *fakeTools, workDir string) *CodeAgent {
	t.Helper()
	base := NewBaseAgent(a2a.AgentCard{ID: "code-agent", Name: "Code"}, gw, tools.run, nil, workDir)
	return NewCodeAgent(base)
}

const samplePlan = `{"files":[
  {"path":"main.py","content":"print('hi')","is_new":true},
  {"path":"util.py","content":"def x(): pass","is_new":true}
],"summary":"hello world script","dependencies":[]}`

func TestCode_WritesFilesFromPlan(t *testing.T) {
	tools := newFakeTools()
	a := codeAgentWith(t, &fakeGateway{reply: samplePlan}, tools, t.TempDir())

	var steps []string
	res := a.HandleTask(context.Background(),
		a2a.Task{ID: "t1", Goal: "write hello world"},
		func(p a2a.Progress) { steps = append(steps, p.Message) })

	if !res.Success {
		t.Fatalf("HandleTask failed: %+v", res.Errors)
	}
	if len(res.Files) != 2 {
		t.Errorf("result files = %v, want 2", res.Files)
	}
	if tools.written["main.py"] != "print('hi')" {
		t.Errorf("main.py content = %q", tools.written["main.py"])
	}
	if !strings.Contains(res.Output, "hello world script") {
		t.Errorf("output missing summary: %q", res.Output)
	}
	if len(steps) < 5 {
		t.Errorf("expected progress at each step, got %d updates", len(steps))
	}
}

func TestCode_ReadsContextFiles(t *testing.T) {
	tools := newFakeTools()
	tools.reads["existing.py"] = "EXISTING CONTENT"
	gw := &fakeGateway{reply: samplePlan}
	a := codeAgentWith(t, gw, tools, t.TempDir())

	a.HandleTask(context.Background(),
		a2a.Task{ID: "t1", Goal: "extend", Files: []string{"existing.py"}}, nil)

	if !tools.calledWith("read_file") {
		t.Error("HandleTask should read the task's context files")
	}
	if !strings.Contains(gw.lastUser, "EXISTING CONTENT") {
		t.Errorf("existing file content not included in prompt:\n%s", gw.lastUser)
	}
}

func TestCode_ReadsAgentsMD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("RULE: use type hints"), 0o600); err != nil {
		t.Fatal(err)
	}
	gw := &fakeGateway{reply: samplePlan}
	a := codeAgentWith(t, gw, newFakeTools(), dir)
	a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "g"}, nil)
	if !strings.Contains(gw.lastUser, "use type hints") {
		t.Errorf("AGENTS.md not included in prompt:\n%s", gw.lastUser)
	}
}

func TestCode_TracksModifiedFiles(t *testing.T) {
	tools := newFakeTools()
	a := codeAgentWith(t, &fakeGateway{reply: samplePlan}, tools, t.TempDir())
	res := a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "g"}, nil)
	want := map[string]bool{"main.py": true, "util.py": true}
	for _, f := range res.Files {
		delete(want, f)
	}
	if len(want) != 0 {
		t.Errorf("result.Files missing %v", want)
	}
}

func TestCode_BadJSONReturnsError(t *testing.T) {
	a := codeAgentWith(t, &fakeGateway{reply: "sure, here is some code!"}, newFakeTools(), t.TempDir())
	res := a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "g"}, nil)
	if res.Success {
		t.Error("non-JSON plan should fail the task")
	}
	if len(res.Errors) == 0 {
		t.Error("expected a parse error")
	}
}

func TestCode_EmptyPlanFails(t *testing.T) {
	a := codeAgentWith(t, &fakeGateway{reply: `{"files":[],"summary":"nothing"}`}, newFakeTools(), t.TempDir())
	res := a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "g"}, nil)
	if res.Success {
		t.Error("empty file plan should fail")
	}
}

func TestCode_GoBuildVerification(t *testing.T) {
	dir := t.TempDir()
	// Presence of go.mod triggers a `go build` verification call.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tools := newFakeTools()
	tools.termOut = "ok" // clean build
	plan := `{"files":[{"path":"main.go","content":"package main","is_new":true}],"summary":"go"}`
	a := codeAgentWith(t, &fakeGateway{reply: plan}, tools, dir)
	res := a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "g"}, nil)
	if !res.Success {
		t.Fatalf("clean build should succeed: %+v", res.Errors)
	}
	if !tools.calledWith("run_terminal") {
		t.Error("go.mod present should trigger a build verification")
	}
}

func TestCode_VerificationFailureReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tools := newFakeTools()
	tools.termOut = "main.go:3: syntax error near foo" // build error
	plan := `{"files":[{"path":"main.go","content":"package main\nfoo","is_new":true}],"summary":"go"}`
	a := codeAgentWith(t, &fakeGateway{reply: plan}, tools, dir)
	res := a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "g"}, nil)
	// Files still written, but the build issue is surfaced in errors.
	if len(res.Errors) == 0 {
		t.Error("build error should be reported")
	}
}

func TestStripFences(t *testing.T) {
	cases := map[string]string{
		"```json\n{\"a\":1}\n```": `{"a":1}`,
		"```\nplain\n```":         "plain",
		"{\"a\":1}":               `{"a":1}`,
	}
	for in, want := range cases {
		if got := stripFences(in); got != want {
			t.Errorf("stripFences(%q) = %q, want %q", in, got, want)
		}
	}
}
