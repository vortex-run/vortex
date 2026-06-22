package specialists

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/vortex-run/vortex/internal/a2a"
)

// TestAgent runs a project's tests and reports pass/fail. It detects the
// project type, runs the suite, parses the output, and (when no tests exist)
// writes basic tests via the AI.
type TestAgent struct {
	*BaseAgent
}

// testSystemPrompt is the Test Agent's focused role definition.
const testSystemPrompt = `You are an expert QA engineer in VORTEX.
Your ONLY job is to test code written by other agents.

Rules:
- Run ALL existing tests first
- Report exact pass/fail counts
- Identify which files caused failures
- Write new tests if coverage is low
- Never modify source code — only test files
- Always run with verbose output
- A task is only done when ALL tests pass

When asked to write tests, return ONLY JSON:
{"files":[{"path":"test_x.py","content":"..."}]}`

// NewTestAgent constructs a TestAgent over a BaseAgent.
func NewTestAgent(base *BaseAgent) *TestAgent {
	base.card.Role = "tester"
	if base.card.Capabilities == nil {
		base.card.Capabilities = []string{"run_tests", "analyze_results", "write_tests"}
	}
	base.SetSystemPrompt(testSystemPrompt)
	return &TestAgent{BaseAgent: base}
}

// TestRunResult captures a parsed test run.
type TestRunResult struct {
	Passed   int
	Failed   int
	Skipped  int
	Failures []string // failed test names + errors
	Output   string   // full output
}

// HandleTask implements the a2a.Agent contract for testing work.
func (a *TestAgent) HandleTask(ctx context.Context, task a2a.Task, progressFn func(a2a.Progress)) a2a.TaskResult {
	result := a2a.NewResult(task.ID, a.card.ID, false)

	// Step 1 — detect project type.
	a.Progress(progressFn, task.ID, "Detecting project type...", 1, 4)
	cmd, kind := a.detectTestCommand()
	if cmd == "" {
		result.Output = "No recognised project type to test"
		result.Success = true // nothing to test is not a failure
		result.Approved = true
		return *result
	}

	// Step 2 — run tests.
	a.Progress(progressFn, task.ID, "Running tests...", 2, 4)
	out, terr := a.RunTool(ctx, "run_terminal", map[string]any{"command": cmd, "cwd": a.workDir})
	if terr != nil {
		result.Errors = []string{"could not run tests: " + terr.Error()}
		return *result
	}
	run := parseTestOutput(kind, toolString(out))

	// Step 3 — analyse / write tests if none found.
	a.Progress(progressFn, task.ID, "Analyzing results...", 3, 4)
	if run.Passed == 0 && run.Failed == 0 {
		a.Progress(progressFn, task.ID, "No tests found — writing basic tests...", 3, 4)
		// Best effort: ask the AI for a basic test, write it, re-run.
		if a.writeBasicTest(ctx, task) {
			out, _ = a.RunTool(ctx, "run_terminal", map[string]any{"command": cmd, "cwd": a.workDir})
			run = parseTestOutput(kind, toolString(out))
		}
	}

	// Step 4 — return result.
	a.Progress(progressFn, task.ID, "Tests complete", 4, 4)
	result.Output = formatTestSummary(run)
	result.Success = run.Failed == 0 && (run.Passed > 0 || run.Skipped > 0 || isEmptyRun(run))
	result.Approved = result.Success
	if run.Failed > 0 {
		result.Files = failingFiles(run)
		result.Errors = run.Failures
	}
	return *result
}

// detectTestCommand returns the test command + project kind by inspecting the
// work dir.
func (a *TestAgent) detectTestCommand() (cmd, kind string) {
	d := a.workDir
	switch {
	case fileExists(filepath.Join(d, "go.mod")):
		return "go test ./... -v", "go"
	case fileExists(filepath.Join(d, "requirements.txt")) || hasPyFiles(d):
		return "python -m pytest -v", "python"
	case fileExists(filepath.Join(d, "package.json")):
		return "npm test", "node"
	case fileExists(filepath.Join(d, "pubspec.yaml")):
		return "flutter test", "flutter"
	case fileExists(filepath.Join(d, "Makefile")):
		return "make test", "make"
	default:
		return "", ""
	}
}

// writeBasicTest asks the AI for a basic test file and writes it. Returns true
// on success.
func (a *TestAgent) writeBasicTest(ctx context.Context, task a2a.Task) bool {
	reply, err := a.Complete(ctx, testSystemPrompt,
		"Write one basic test for this goal:\n"+task.Goal+"\nReturn JSON with a test file.")
	if err != nil {
		return false
	}
	var plan struct {
		Files []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"files"`
	}
	if jsonUnmarshalFences(reply, &plan) != nil || len(plan.Files) == 0 {
		return false
	}
	wrote := false
	for _, f := range plan.Files {
		if _, werr := a.RunTool(ctx, "write_file", map[string]any{
			"path": f.Path, "content": f.Content, "create_dirs": true}); werr == nil {
			wrote = true
		}
	}
	return wrote
}

// --- output parsing ---------------------------------------------------------

// parseTestOutput dispatches to the per-runner parser.
func parseTestOutput(kind, output string) TestRunResult {
	switch kind {
	case "go":
		return parseGoTestOutput(output)
	case "python":
		return parsePytestOutput(output)
	default:
		return parseGenericOutput(output)
	}
}

var (
	goPassRe = regexp.MustCompile(`(?m)^--- PASS:`)
	goFailRe = regexp.MustCompile(`(?m)^--- FAIL:\s+(\S+)`)
	goSkipRe = regexp.MustCompile(`(?m)^--- SKIP:`)
)

// parseGoTestOutput counts PASS/FAIL/SKIP lines from `go test -v`.
func parseGoTestOutput(output string) TestRunResult {
	r := TestRunResult{Output: output}
	r.Passed = len(goPassRe.FindAllString(output, -1))
	r.Skipped = len(goSkipRe.FindAllString(output, -1))
	for _, m := range goFailRe.FindAllStringSubmatch(output, -1) {
		r.Failed++
		r.Failures = append(r.Failures, m[1])
	}
	return r
}

var (
	pyFailedRe  = regexp.MustCompile(`(\d+)\s+failed`)
	pyPassedRe  = regexp.MustCompile(`(\d+)\s+passed`)
	pySkippedRe = regexp.MustCompile(`(\d+)\s+skipped`)
	pyFailLine  = regexp.MustCompile(`(?m)^FAILED\s+(\S+)`)
)

// parsePytestOutput reads the pytest summary line and FAILED entries.
func parsePytestOutput(output string) TestRunResult {
	r := TestRunResult{Output: output}
	r.Passed = firstInt(pyPassedRe, output)
	r.Failed = firstInt(pyFailedRe, output)
	r.Skipped = firstInt(pySkippedRe, output)
	for _, m := range pyFailLine.FindAllStringSubmatch(output, -1) {
		r.Failures = append(r.Failures, m[1])
	}
	return r
}

// parseGenericOutput is a fallback: a non-zero "ok"/"pass" with no "fail".
func parseGenericOutput(output string) TestRunResult {
	r := TestRunResult{Output: output}
	low := strings.ToLower(output)
	if strings.Contains(low, "fail") || strings.Contains(low, "error") {
		r.Failed = 1
		r.Failures = []string{"test run reported failures"}
	} else if strings.Contains(low, "pass") || strings.Contains(low, "ok") {
		r.Passed = 1
	}
	return r
}

// formatTestSummary renders a one-line pass summary or a failure list.
func formatTestSummary(r TestRunResult) string {
	total := r.Passed + r.Failed
	if r.Failed == 0 {
		if total == 0 {
			return "✓ No tests to run"
		}
		return fmt.Sprintf("✓ %d/%d tests pass", r.Passed, total)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "✗ %d/%d tests failed:\n", r.Failed, total)
	for _, f := range r.Failures {
		b.WriteString("  - " + f + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// failingFiles extracts likely source files from failure entries (best effort).
func failingFiles(r TestRunResult) []string {
	var files []string
	seen := map[string]bool{}
	fileRe := regexp.MustCompile(`([\w/.\\-]+\.(go|py|js|ts))`)
	for _, f := range append(r.Failures, r.Output) {
		for _, m := range fileRe.FindAllStringSubmatch(f, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				files = append(files, m[1])
			}
		}
	}
	return files
}

// isEmptyRun reports whether nothing ran (used to treat "no tests" as success).
func isEmptyRun(r TestRunResult) bool { return r.Passed == 0 && r.Failed == 0 && r.Skipped == 0 }

// firstInt returns the first capture of re in s as an int (0 if absent).
func firstInt(re *regexp.Regexp, s string) int {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// hasPyFiles reports whether dir contains any .py file at the top level.
func hasPyFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".py") {
			return true
		}
	}
	return false
}

// jsonUnmarshalFences strips code fences then unmarshals into v.
func jsonUnmarshalFences(s string, v any) error {
	return json.Unmarshal([]byte(stripFences(s)), v)
}
