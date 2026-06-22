package specialists

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vortex-run/vortex/internal/a2a"
)

// CodeAgent writes and edits code. It reads context (task files + AGENTS.md),
// plans an implementation via the AI gateway, writes the files, and verifies
// the result compiles where possible.
type CodeAgent struct {
	*BaseAgent
}

// codeSystemPrompt is the Code Agent's focused role definition.
const codeSystemPrompt = `You are an expert software engineer in VORTEX.
Your ONLY job is to write clean, working code.

Rules:
- Always read existing files before editing them
- Follow the project's code style exactly
- Write code that works on the first attempt
- Use the project's existing patterns
- Add comments for complex logic
- Never delete existing functionality unless asked

Return ONLY valid JSON describing the files to create or modify, no prose:
{"files": [{"path": "main.py", "content": "...", "is_new": true}], "summary": "what was built", "dependencies": []}`

// NewCodeAgent constructs a CodeAgent over a BaseAgent.
func NewCodeAgent(base *BaseAgent) *CodeAgent {
	base.card.Role = "coder"
	if base.card.Capabilities == nil {
		base.card.Capabilities = []string{"write_code", "edit_file", "read_file", "search_files", "git_status", "git_diff"}
	}
	return &CodeAgent{BaseAgent: base}
}

// codePlan is the AI's planned file operations.
type codePlan struct {
	Files []struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		IsNew   bool   `json:"is_new"`
	} `json:"files"`
	Summary      string   `json:"summary"`
	Dependencies []string `json:"dependencies"`
}

// HandleTask implements the a2a.Agent contract for coding work.
func (a *CodeAgent) HandleTask(ctx context.Context, task a2a.Task, progressFn func(a2a.Progress)) a2a.TaskResult {
	result := a2a.NewResult(task.ID, a.card.ID, false)

	// Step 1 — understand context.
	a.Progress(progressFn, task.ID, "Reading project context...", 1, 5)
	fileContents := a.readFiles(ctx, task.Files)
	agentsMD := a.readAgentsMD()
	dirStructure := a.listDir(ctx)

	// Step 2 — plan the implementation.
	a.Progress(progressFn, task.ID, "Planning implementation...", 2, 5)
	prompt := buildCodePrompt(task, fileContents, dirStructure, agentsMD)
	reply, err := a.Complete(ctx, codeSystemPrompt, prompt)
	if err != nil {
		result.Errors = []string{"planning failed: " + err.Error()}
		return *result
	}
	var plan codePlan
	if perr := json.Unmarshal([]byte(stripFences(reply)), &plan); perr != nil {
		result.Errors = []string{"could not parse plan: " + perr.Error()}
		result.Output = reply
		return *result
	}
	if len(plan.Files) == 0 {
		result.Errors = []string{"plan produced no files"}
		return *result
	}

	// Step 3 — execute file operations.
	a.Progress(progressFn, task.ID, "Writing code...", 3, 5)
	var written []string
	for _, f := range plan.Files {
		a.Progress(progressFn, task.ID, "Writing "+f.Path+"...", 3, 5)
		if _, terr := a.RunTool(ctx, "write_file", map[string]any{
			"path": f.Path, "content": f.Content, "create_dirs": true,
		}); terr != nil {
			result.Errors = append(result.Errors, "write "+f.Path+": "+terr.Error())
			continue
		}
		written = append(written, f.Path)
	}
	if len(written) == 0 {
		result.Output = "No files were written"
		return *result
	}

	// Step 4 — verify (best effort; failures are reported, not fatal).
	a.Progress(progressFn, task.ID, "Verifying code...", 4, 5)
	if verr := a.verify(ctx, written); verr != "" {
		result.Errors = append(result.Errors, verr)
	}

	// Step 5 — done.
	a.Progress(progressFn, task.ID, "Complete", 5, 5)
	result.Success = true
	result.Files = written
	result.Output = fmt.Sprintf("Created/modified %d file(s): %s", len(written), strings.Join(written, ", "))
	if plan.Summary != "" {
		result.Output += "\n" + plan.Summary
	}
	return *result
}

// readFiles reads each path via the read_file tool, returning path→content.
func (a *CodeAgent) readFiles(ctx context.Context, paths []string) map[string]string {
	out := map[string]string{}
	for _, p := range paths {
		res, err := a.RunTool(ctx, "read_file", map[string]any{"path": p})
		if err != nil {
			continue
		}
		out[p] = toolString(res)
	}
	return out
}

// readAgentsMD returns the contents of AGENTS.md in the work dir, or "".
func (a *CodeAgent) readAgentsMD() string {
	data, err := os.ReadFile(filepath.Join(a.workDir, "AGENTS.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

// listDir returns a short directory listing via the list_directory tool.
func (a *CodeAgent) listDir(ctx context.Context) string {
	res, err := a.RunTool(ctx, "list_directory", map[string]any{"path": "."})
	if err != nil {
		return ""
	}
	return toolString(res)
}

// verify runs a language build check on the written files, returning a
// non-empty error description on failure (best effort; missing toolchains are
// not errors).
func (a *CodeAgent) verify(ctx context.Context, files []string) string {
	switch {
	case fileExists(filepath.Join(a.workDir, "go.mod")):
		res, err := a.RunTool(ctx, "run_terminal", map[string]any{"command": "go build ./...", "cwd": a.workDir})
		if err != nil {
			return "" // terminal gated/unavailable; skip verification
		}
		if out := toolString(res); strings.Contains(strings.ToLower(out), "error") {
			return "go build reported issues: " + firstLines(out, 5)
		}
	case anyHasSuffix(files, ".py"):
		var pys []string
		for _, f := range files {
			if strings.HasSuffix(f, ".py") {
				pys = append(pys, f)
			}
		}
		res, err := a.RunTool(ctx, "run_terminal", map[string]any{
			"command": "python -m py_compile " + strings.Join(pys, " "), "cwd": a.workDir,
		})
		if err != nil {
			return ""
		}
		if out := toolString(res); strings.Contains(strings.ToLower(out), "error") {
			return "python compile reported issues: " + firstLines(out, 5)
		}
	}
	return ""
}

// buildCodePrompt assembles a focused prompt for the coder.
func buildCodePrompt(task a2a.Task, files map[string]string, dirStructure, agentsMD string) string {
	var b strings.Builder
	b.WriteString("Goal: " + task.Goal + "\n\n")
	if task.Context != "" {
		b.WriteString("Context:\n" + task.Context + "\n\n")
	}
	if agentsMD != "" {
		b.WriteString("Project rules (AGENTS.md):\n" + agentsMD + "\n\n")
	}
	if dirStructure != "" {
		b.WriteString("Directory structure:\n" + dirStructure + "\n\n")
	}
	if len(files) > 0 {
		b.WriteString("Existing files:\n")
		for path, content := range files {
			b.WriteString("--- " + path + " ---\n" + content + "\n\n")
		}
	}
	if len(task.Constraints) > 0 {
		b.WriteString("Constraints:\n- " + strings.Join(task.Constraints, "\n- ") + "\n\n")
	}
	b.WriteString("Produce the file operations as JSON.")
	return b.String()
}

// --- shared helpers ---------------------------------------------------------

// stripFences removes a leading/trailing ```lang fence from an AI reply.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "\n"), "```")
	return strings.TrimSpace(s)
}

// ToolResultString coerces a tool result into a string (exported for the team
// package's checkpoint previews).
func ToolResultString(res any) string { return toolString(res) }

// toolString coerces a tool result into a string (tools return strings or maps
// with an "output"/"content" field).
func toolString(res any) string {
	switch v := res.(type) {
	case string:
		return v
	case map[string]any:
		for _, k := range []string{"output", "content", "result", "stdout"} {
			if s, ok := v[k].(string); ok {
				return s
			}
		}
		b, _ := json.Marshal(v)
		return string(b)
	default:
		return fmt.Sprint(res)
	}
}

// fileExists reports whether path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// anyHasSuffix reports whether any string ends with suffix.
func anyHasSuffix(xs []string, suffix string) bool {
	for _, x := range xs {
		if strings.HasSuffix(x, suffix) {
			return true
		}
	}
	return false
}

// firstLines returns the first n lines of s.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
