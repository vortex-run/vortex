package specialists

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/a2a"
)

func testAgentWith(t *testing.T, gw AIGateway, tools *fakeTools, workDir string) *TestAgent {
	t.Helper()
	base := NewBaseAgent(a2a.AgentCard{ID: "test-agent", Name: "Test"}, gw, tools.run, nil, workDir)
	return NewTestAgent(base)
}

func TestTest_DetectsGoProject(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600)
	a := testAgentWith(t, &fakeGateway{}, newFakeTools(), dir)
	cmd, kind := a.detectTestCommand()
	if kind != "go" || !strings.Contains(cmd, "go test") {
		t.Errorf("detect = %q/%q, want go test", cmd, kind)
	}
}

func TestTest_DetectsPythonProject(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "main.py"), []byte("print(1)"), 0o600)
	a := testAgentWith(t, &fakeGateway{}, newFakeTools(), dir)
	cmd, kind := a.detectTestCommand()
	if kind != "python" || !strings.Contains(cmd, "pytest") {
		t.Errorf("detect = %q/%q, want pytest", cmd, kind)
	}
}

func TestParseGoTestOutput(t *testing.T) {
	output := `=== RUN   TestA
--- PASS: TestA (0.00s)
=== RUN   TestB
--- PASS: TestB (0.00s)
=== RUN   TestC
--- FAIL: TestC (0.01s)
    foo_test.go:12: bad
=== RUN   TestD
--- SKIP: TestD (0.00s)
FAIL`
	r := parseGoTestOutput(output)
	if r.Passed != 2 {
		t.Errorf("passed = %d, want 2", r.Passed)
	}
	if r.Failed != 1 || len(r.Failures) != 1 || r.Failures[0] != "TestC" {
		t.Errorf("failed = %d %v, want 1 [TestC]", r.Failed, r.Failures)
	}
	if r.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", r.Skipped)
	}
}

func TestParsePytestOutput(t *testing.T) {
	output := `tests/test_auth.py::test_login PASSED
tests/test_auth.py::test_logout FAILED
FAILED tests/test_auth.py::test_logout - assert 1 == 2
======= 11 passed, 1 failed, 2 skipped in 0.34s =======`
	r := parsePytestOutput(output)
	if r.Passed != 11 || r.Failed != 1 || r.Skipped != 2 {
		t.Errorf("counts = %d/%d/%d, want 11/1/2", r.Passed, r.Failed, r.Skipped)
	}
	if len(r.Failures) != 1 {
		t.Errorf("failures = %v, want 1", r.Failures)
	}
}

func TestTest_AllPassApproved(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600)
	tools := newFakeTools()
	tools.termOut = "--- PASS: TestA (0.00s)\n--- PASS: TestB (0.00s)\nok"
	a := testAgentWith(t, &fakeGateway{}, tools, dir)
	res := a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "test"}, nil)
	if !res.Success || !res.Approved {
		t.Errorf("all-pass should be Success+Approved: %+v", res)
	}
	if !strings.Contains(res.Output, "2/2 tests pass") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestTest_AnyFailNotApproved(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600)
	tools := newFakeTools()
	tools.termOut = "--- PASS: TestA (0.00s)\n--- FAIL: TestB (0.01s)\n    auth_test.go:5: nope\nFAIL"
	a := testAgentWith(t, &fakeGateway{}, tools, dir)
	res := a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "test"}, nil)
	if res.Success || res.Approved {
		t.Error("a failing test should not be Success/Approved")
	}
	if !strings.Contains(res.Output, "1/2 tests failed") {
		t.Errorf("output = %q", res.Output)
	}
	if len(res.Files) == 0 {
		t.Error("failing files should be reported (auth_test.go)")
	}
}

func TestTest_NoTestsWritesBasic(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "main.py"), []byte("print(1)"), 0o600)
	tools := newFakeTools()
	// First run: no tests. After writing, second run passes.
	tools.termOut = "no tests ran"
	gw := &fakeGateway{reply: `{"files":[{"path":"test_main.py","content":"def test_x(): assert True"}]}`}
	a := testAgentWith(t, gw, tools, dir)

	a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "test"}, nil)

	// The no-tests path asks the AI for a basic test and writes it.
	if !tools.calledWith("write_file") {
		t.Error("no-tests path should write a basic test")
	}
}

func TestFormatTestSummary(t *testing.T) {
	if s := formatTestSummary(TestRunResult{Passed: 12}); s != "✓ 12/12 tests pass" {
		t.Errorf("pass summary = %q", s)
	}
	if s := formatTestSummary(TestRunResult{Passed: 9, Failed: 3, Failures: []string{"TestAuth"}}); !strings.Contains(s, "3/12 tests failed") || !strings.Contains(s, "TestAuth") {
		t.Errorf("fail summary = %q", s)
	}
	if s := formatTestSummary(TestRunResult{}); !strings.Contains(s, "No tests") {
		t.Errorf("empty summary = %q", s)
	}
}

func TestTest_UnknownProjectIsSuccess(t *testing.T) {
	a := testAgentWith(t, &fakeGateway{}, newFakeTools(), t.TempDir())
	res := a.HandleTask(context.Background(), a2a.Task{ID: "t1", Goal: "test"}, nil)
	if !res.Success || !res.Approved {
		t.Errorf("unknown project (nothing to test) should pass: %+v", res)
	}
}
