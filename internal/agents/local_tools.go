package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Local filesystem + terminal tools give the agent real machine access (like
// Claude Code). Read-only tools (list_directory, read_file) run freely;
// mutating tools (write_file, edit_file, run_terminal, create_project) return an
// *ApprovalError and only execute after a human approves AND the request clears
// the guardrails below. This deliberately re-opens audit findings C2/C3 behind
// an approval gate, with guardrails as defence in depth (owner-approved).

// ErrDangerousAction is returned when an action matches a catastrophic,
// always-blocked pattern (e.g. rm -rf /, a fork bomb, formatting a disk, or
// writing into a Windows system directory). These are refused EVEN WITH user
// approval — they are non-recoverable system destruction, not normal edits.
// There is no path/working-directory confinement: the approval gate is the
// security control (the Claude Code model). Any other path is allowed once the
// user approves the action.
var ErrDangerousAction = fmt.Errorf("agents: action blocked (catastrophic system destruction)")

// dangerousCommandPatterns are commands refused even after approval.
var dangerousCommandPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brm\s+-rf\s+/(\s|$)`),          // rm -rf /
	regexp.MustCompile(`(?i)\brm\s+-rf\s+/\*`),              // rm -rf /*
	regexp.MustCompile(`(?i)\brm\s+-rf\s+\*`),               // rm -rf * with no specific path
	regexp.MustCompile(`(?i)\brm\s+-rf\s+~`),                // rm -rf ~ (home)
	regexp.MustCompile(`(?i)\bdel\s+/[fsq].*[A-Za-z]:\\?$`), // del /f /s /q C:\  (root of a drive)
	regexp.MustCompile(`(?i)\bdel\s+/[fsq].*\b[CD]:\\?(\s|$)`),
	regexp.MustCompile(`(?i)\bformat\s+[A-Za-z]:`),         // format C: / format D:
	regexp.MustCompile(`:\(\)\s*\{\s*:\|:&\s*\}\s*;`),      // fork bomb :(){ :|:& };:
	regexp.MustCompile(`(?i)\bmkfs\b`),                     // format a filesystem
	regexp.MustCompile(`(?i)\bdd\b.*\bif=/dev/zero`),       // dd if=/dev/zero …
	regexp.MustCompile(`(?i)\bdd\b.*\bof=/dev/`),           // dd to a device
	regexp.MustCompile(`(?i)>\s*/dev/sd[a-z]`),             // overwrite a disk
	regexp.MustCompile(`(?i)\bchmod\s+-R\s+777\s+/(\s|$)`), // chmod -R 777 /
}

// isDangerousCommand reports whether a command matches a blocked pattern.
func isDangerousCommand(command string) bool {
	for _, re := range dangerousCommandPatterns {
		if re.MatchString(command) {
			return true
		}
	}
	return false
}

// protectedWritePrefixes are absolute path prefixes that may never be written
// to or edited, even with approval — overwriting them bricks the OS.
var protectedWritePrefixes = []string{
	`c:\windows\system32`,
	`c:\windows`,
}

// isProtectedPath reports whether p targets a protected system directory. It
// matches the Windows system paths regardless of host OS (so the check is
// deterministic in CI on Linux too): it normalises separators and looks for a
// "c:\windows" / "c:\windows\system32" prefix in the path as written.
func isProtectedPath(p string) bool {
	norm := strings.ToLower(strings.ReplaceAll(p, "/", `\`))
	for _, prefix := range protectedWritePrefixes {
		if norm == prefix || strings.HasPrefix(norm, prefix+`\`) {
			return true
		}
	}
	return false
}

// LocalFSConfig configures the local FS/terminal tools. There is NO path
// confinement — the approval gate is the security model. Root is informational
// only: it is the base for resolving *relative* paths (defaults to the process
// working directory) and is shown in the TUI top bar.
type LocalFSConfig struct {
	// Root is the base for resolving relative paths (default: os.Getwd). It does
	// NOT restrict where files may be written; absolute paths anywhere are
	// allowed once the user approves the action.
	Root string
	// RequireApproval gates mutating tools (default true via NewLocalTools).
	RequireApproval bool
}

// resolveLocal resolves p to an absolute path. Relative paths resolve against
// Root (or the process cwd). Absolute paths are accepted as-is. No path is
// rejected for being "outside" anything — only catastrophic system paths are
// blocked, by isProtectedPath at the write site.
func (cfg LocalFSConfig) resolveLocal(p string) (string, error) {
	if p == "" {
		p = "."
	}
	if !filepath.IsAbs(p) {
		base := cfg.Root
		if base == "" {
			base, _ = os.Getwd()
		}
		p = filepath.Join(base, p)
	}
	return filepath.Abs(p)
}

// --- ListDirectoryTool (read-only) ------------------------------------------

// ListDirectoryTool lists a directory's entries. Read-only, no approval.
type ListDirectoryTool struct{ cfg LocalFSConfig }

// Name returns the tool name.
func (ListDirectoryTool) Name() string { return "list_directory" }

// Description returns a human-readable summary.
func (ListDirectoryTool) Description() string { return "List a local directory" }

// Execute lists params["path"] (default: working dir).
func (t ListDirectoryTool) Execute(_ context.Context, params map[string]any) (any, error) {
	p, _ := params["path"].(string)
	abs, err := t.cfg.resolveLocal(p)
	if err != nil {
		return nil, err
	}
	dirents, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	entries := make([]map[string]any, 0, len(dirents))
	for _, d := range dirents {
		info, ierr := d.Info()
		size := int64(0)
		mod := ""
		if ierr == nil {
			size = info.Size()
			mod = info.ModTime().Format(time.RFC3339)
		}
		typ := "file"
		if d.IsDir() {
			typ = "dir"
		}
		entries = append(entries, map[string]any{
			"name": d.Name(), "type": typ, "size": size, "modified": mod,
		})
	}
	return map[string]any{"path": abs, "entries": entries}, nil
}

// --- ReadLocalFileTool (read-only) ------------------------------------------

// ReadLocalFileTool reads a real file. Read-only, no approval.
type ReadLocalFileTool struct{ cfg LocalFSConfig }

// Name returns the tool name.
func (ReadLocalFileTool) Name() string { return "read_file" }

// Description returns a human-readable summary.
func (ReadLocalFileTool) Description() string { return "Read a local file" }

// Execute reads params["path"].
func (t ReadLocalFileTool) Execute(_ context.Context, params map[string]any) (any, error) {
	p, err := strParam(params, "path")
	if err != nil {
		return nil, err
	}
	abs, err := t.cfg.resolveLocal(p)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs) //nolint:gosec // user-approved local read
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"content": string(data), "size": len(data),
		"path": abs, "ext": strings.TrimPrefix(filepath.Ext(abs), "."),
	}, nil
}

// --- WriteLocalFileTool (approval) ------------------------------------------

// WriteLocalFileTool writes a real file. Requires approval.
type WriteLocalFileTool struct {
	cfg             LocalFSConfig
	RequireApproval bool
}

// Name returns the tool name.
func (WriteLocalFileTool) Name() string { return "write_file" }

// Description returns a human-readable summary.
func (WriteLocalFileTool) Description() string { return "Write a local file (approval required)" }

// Execute writes params["path"] with params["content"], optionally creating
// parent dirs (params["create_dirs"]). Returns an *ApprovalError when approval
// is required.
func (t WriteLocalFileTool) Execute(_ context.Context, params map[string]any) (any, error) {
	p, err := strParam(params, "path")
	if err != nil {
		return nil, err
	}
	content, err := strParam(params, "content")
	if err != nil {
		return nil, err
	}
	abs, err := t.cfg.resolveLocal(p)
	if err != nil {
		return nil, err
	}
	if isProtectedPath(abs) || isProtectedPath(p) {
		return nil, fmt.Errorf("%w: %q is a protected system path", ErrDangerousAction, abs)
	}
	if t.RequireApproval {
		return nil, &ApprovalError{Request: ApprovalRequest{
			Tool:        t.Name(),
			Description: "create/overwrite file " + abs,
			Preview:     filePreview(abs, content),
			Params:      params,
		}}
	}
	createDirs, _ := params["create_dirs"].(bool)
	if createDirs {
		if mkerr := os.MkdirAll(filepath.Dir(abs), 0o755); mkerr != nil { //nolint:gosec // user dirs
			return nil, mkerr
		}
	}
	if werr := os.WriteFile(abs, []byte(content), 0o644); werr != nil { //nolint:gosec // user-approved write
		return nil, werr
	}
	return map[string]any{"path": abs, "bytes_written": len(content)}, nil
}

// --- EditFileTool (approval) ------------------------------------------------

// EditFileTool replaces an exact string in a real file. Requires approval.
type EditFileTool struct {
	cfg             LocalFSConfig
	RequireApproval bool
}

// Name returns the tool name.
func (EditFileTool) Name() string { return "edit_file" }

// Description returns a human-readable summary.
func (EditFileTool) Description() string {
	return "Edit a local file by string replace (approval required)"
}

// Execute replaces params["old_str"] with params["new_str"] in params["path"].
func (t EditFileTool) Execute(_ context.Context, params map[string]any) (any, error) {
	p, err := strParam(params, "path")
	if err != nil {
		return nil, err
	}
	oldStr, err := strParam(params, "old_str")
	if err != nil {
		return nil, err
	}
	newStr, err := strParam(params, "new_str")
	if err != nil {
		return nil, err
	}
	abs, err := t.cfg.resolveLocal(p)
	if err != nil {
		return nil, err
	}
	if isProtectedPath(abs) || isProtectedPath(p) {
		return nil, fmt.Errorf("%w: %q is a protected system path", ErrDangerousAction, abs)
	}
	data, err := os.ReadFile(abs) //nolint:gosec // user-approved local read
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(data), oldStr) {
		return nil, fmt.Errorf("agents: old_str not found in %s", abs)
	}
	if t.RequireApproval {
		return nil, &ApprovalError{Request: ApprovalRequest{
			Tool:        t.Name(),
			Description: "edit file " + abs,
			Preview:     diffPreview(oldStr, newStr),
			Params:      params,
		}}
	}
	updated := strings.Replace(string(data), oldStr, newStr, 1)
	if werr := os.WriteFile(abs, []byte(updated), 0o644); werr != nil { //nolint:gosec // approved
		return nil, werr
	}
	return map[string]any{
		"path": abs, "lines_changed": strings.Count(oldStr, "\n") + 1,
		"preview": diffPreview(oldStr, newStr),
	}, nil
}

// --- RunTerminalTool (approval) ---------------------------------------------

// RunTerminalTool runs an arbitrary command (approval required, guardrailed).
type RunTerminalTool struct {
	cfg             LocalFSConfig
	RequireApproval bool
}

// Name returns the tool name.
func (RunTerminalTool) Name() string { return "run_terminal" }

// Description returns a human-readable summary.
func (RunTerminalTool) Description() string { return "Run a terminal command (approval required)" }

// Execute runs params["command"] in params["cwd"] with params["timeout"] (s).
func (t RunTerminalTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	command, err := strParam(params, "command")
	if err != nil {
		return nil, err
	}
	if isDangerousCommand(command) {
		return nil, fmt.Errorf("%w: %q", ErrDangerousAction, command)
	}
	cwd, _ := params["cwd"].(string)
	resolvedCwd := t.cfg.Root
	if cwd != "" {
		resolvedCwd, err = t.cfg.resolveLocal(cwd)
		if err != nil {
			return nil, err
		}
	}
	if t.RequireApproval {
		return nil, &ApprovalError{Request: ApprovalRequest{
			Tool:        t.Name(),
			Description: "run: " + command,
			Preview:     commandPreview(resolvedCwd, command),
			Params:      params,
		}}
	}
	timeout := 30 * time.Second
	if v, ok := params["timeout"].(float64); ok && v > 0 {
		timeout = time.Duration(v) * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name, args := shellCommand(command)
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = resolvedCwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	runErr := cmd.Run()
	exit := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exit = ee.ExitCode()
		} else if cctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("agents: command timed out after %s", timeout)
		} else {
			return nil, runErr
		}
	}
	return map[string]any{
		"stdout": stdout.String(), "stderr": stderr.String(),
		"exit_code": exit, "duration_ms": time.Since(start).Milliseconds(),
	}, nil
}

// --- CreateProjectTool (approval) -------------------------------------------

// CreateProjectTool scaffolds a project (approval required).
type CreateProjectTool struct {
	cfg             LocalFSConfig
	RequireApproval bool
}

// Name returns the tool name.
func (CreateProjectTool) Name() string { return "create_project" }

// Description returns a human-readable summary.
func (CreateProjectTool) Description() string { return "Create a project skeleton (approval required)" }

// projectFiles returns the file map for a project type.
func projectFiles(name, typ, description string) map[string]string {
	readme := "# " + name + "\n\n" + description + "\n"
	switch typ {
	case "go":
		return map[string]string{
			"go.mod":    "module " + name + "\n\ngo 1.26\n",
			"main.go":   "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"" + name + "\")\n}\n",
			"README.md": readme,
		}
	case "node":
		return map[string]string{
			"package.json": "{\n  \"name\": \"" + name + "\",\n  \"version\": \"0.1.0\",\n  \"main\": \"index.js\"\n}\n",
			"index.js":     "console.log(\"" + name + "\");\n",
			"README.md":    readme,
		}
	case "flutter":
		return map[string]string{
			"pubspec.yaml": "name: " + name + "\n",
			"README.md":    readme,
		}
	default: // python
		return map[string]string{
			"main.py":          "def main():\n    print(\"" + name + "\")\n\n\nif __name__ == \"__main__\":\n    main()\n",
			"requirements.txt": "",
			"README.md":        readme,
		}
	}
}

// Execute creates the project skeleton at params["path"]/params["name"].
func (t CreateProjectTool) Execute(_ context.Context, params map[string]any) (any, error) {
	name, err := strParam(params, "name")
	if err != nil {
		return nil, err
	}
	typ, _ := params["type"].(string)
	if typ == "" {
		typ = "python"
	}
	description, _ := params["description"].(string)
	path, _ := params["path"].(string)
	rawDir := filepath.Join(path, name)
	dir, err := t.cfg.resolveLocal(rawDir)
	if err != nil {
		return nil, err
	}
	if isProtectedPath(dir) || isProtectedPath(rawDir) {
		return nil, fmt.Errorf("%w: %q is a protected system path", ErrDangerousAction, dir)
	}
	files := projectFiles(name, typ, description)

	if t.RequireApproval {
		return nil, &ApprovalError{Request: ApprovalRequest{
			Tool:        t.Name(),
			Description: fmt.Sprintf("create %s project %q at %s", typ, name, dir),
			Preview:     treePreview(dir, files),
			Params:      params,
		}}
	}
	if mkerr := os.MkdirAll(dir, 0o755); mkerr != nil { //nolint:gosec // user dir
		return nil, mkerr
	}
	created := make([]string, 0, len(files))
	for fname, content := range files {
		full := filepath.Join(dir, fname)
		if werr := os.WriteFile(full, []byte(content), 0o644); werr != nil { //nolint:gosec // approved
			return nil, werr
		}
		created = append(created, fname)
	}
	return map[string]any{"path": dir, "type": typ, "files": created}, nil
}

// --- preview / shell helpers ------------------------------------------------

// filePreview renders a boxed file-content preview.
func filePreview(path, content string) string {
	var b strings.Builder
	b.WriteString("📄 " + path + "\n")
	b.WriteString("┌" + strings.Repeat("─", 40) + "\n")
	for _, line := range strings.Split(content, "\n") {
		b.WriteString("│ " + line + "\n")
	}
	b.WriteString("└" + strings.Repeat("─", 40))
	return b.String()
}

// diffPreview renders a +/- unified-ish preview.
func diffPreview(oldStr, newStr string) string {
	var b strings.Builder
	for _, l := range strings.Split(oldStr, "\n") {
		b.WriteString("- " + l + "\n")
	}
	for _, l := range strings.Split(newStr, "\n") {
		b.WriteString("+ " + l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// commandPreview renders a command + cwd preview.
func commandPreview(cwd, command string) string {
	if cwd == "" {
		cwd = "(working dir)"
	}
	return "📂 " + cwd + "\n$ " + command
}

// treePreview renders the file tree a project would create.
func treePreview(dir string, files map[string]string) string {
	var b strings.Builder
	b.WriteString("📁 " + dir + "\n")
	for fname := range files {
		b.WriteString("  • " + fname + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// shellCommand wraps a command string for the platform shell.
func shellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", command}
	}
	return "sh", []string{"-c", command}
}

// NewLocalTools returns the local FS + terminal tools bound to cfg, with
// approval REQUIRED on all mutating tools.
func NewLocalTools(cfg LocalFSConfig) []Tool {
	cfg.RequireApproval = true
	tools := []Tool{
		ListDirectoryTool{cfg: cfg},
		ReadLocalFileTool{cfg: cfg},
		WriteLocalFileTool{cfg: cfg, RequireApproval: true},
		EditFileTool{cfg: cfg, RequireApproval: true},
		RunTerminalTool{cfg: cfg, RequireApproval: true},
		CreateProjectTool{cfg: cfg, RequireApproval: true},
	}
	tools = append(tools, gitTools(cfg)...)
	tools = append(tools, searchTools(cfg)...)
	return tools
}
