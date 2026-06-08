package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localCfg(t *testing.T) (LocalFSConfig, string) {
	t.Helper()
	dir := t.TempDir()
	return LocalFSConfig{Root: dir}, dir
}

func TestListDirectory_ReadsEntries(t *testing.T) {
	cfg, dir := localCfg(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ListDirectoryTool{cfg: cfg}.Execute(context.Background(), map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := res.(map[string]any)
	entries := m["entries"].([]map[string]any)
	if len(entries) != 1 || entries[0]["name"] != "a.txt" {
		t.Errorf("entries = %+v, want [a.txt]", entries)
	}
}

func TestReadLocalFile_ReadsContent(t *testing.T) {
	cfg, dir := localCfg(t)
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o600)
	res, err := ReadLocalFileTool{cfg: cfg}.Execute(context.Background(), map[string]any{"path": "main.go"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := res.(map[string]any)
	if m["content"] != "package main" || m["ext"] != "go" {
		t.Errorf("read result = %+v", m)
	}
}

func TestReadLocalFile_OutsideRootRejected(t *testing.T) {
	cfg, _ := localCfg(t)
	_, err := ReadLocalFileTool{cfg: cfg}.Execute(context.Background(), map[string]any{"path": "../../etc/passwd"})
	if !errors.Is(err, ErrDangerousAction) {
		t.Errorf("read outside root: err = %v, want ErrDangerousAction", err)
	}
}

func TestWriteLocalFile_RequiresApproval(t *testing.T) {
	cfg, _ := localCfg(t)
	_, err := WriteLocalFileTool{cfg: cfg, RequireApproval: true}.Execute(context.Background(),
		map[string]any{"path": "x.txt", "content": "data"})
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("write should require approval, got %v", err)
	}
	if ae.Request.Tool != "write_file" || !strings.Contains(ae.Request.Preview, "data") {
		t.Errorf("approval request = %+v", ae.Request)
	}
}

func TestWriteLocalFile_ExecutesAfterApproval(t *testing.T) {
	cfg, dir := localCfg(t)
	res, err := WriteLocalFileTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"path": "sub/x.txt", "content": "hello", "create_dirs": true})
	if err != nil {
		t.Fatalf("approved write: %v", err)
	}
	if res.(map[string]any)["bytes_written"].(int) != 5 {
		t.Errorf("bytes_written = %v, want 5", res.(map[string]any)["bytes_written"])
	}
	data, _ := os.ReadFile(filepath.Join(dir, "sub", "x.txt"))
	if string(data) != "hello" {
		t.Errorf("file content = %q", data)
	}
}

func TestWriteLocalFile_OutsideRootRejected(t *testing.T) {
	cfg, _ := localCfg(t)
	_, err := WriteLocalFileTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"path": "../escape.txt", "content": "x"})
	if !errors.Is(err, ErrDangerousAction) {
		t.Errorf("write outside root: err = %v, want ErrDangerousAction", err)
	}
}

func TestEditFile_RequiresApprovalAndApplies(t *testing.T) {
	cfg, dir := localCfg(t)
	_ = os.WriteFile(filepath.Join(dir, "f.go"), []byte("func add(a, b)"), 0o600)

	// Approval gate first.
	_, err := EditFileTool{cfg: cfg, RequireApproval: true}.Execute(context.Background(),
		map[string]any{"path": "f.go", "old_str": "func add(a, b)", "new_str": "func add(a, b, c)"})
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("edit should require approval, got %v", err)
	}
	if !strings.Contains(ae.Request.Preview, "+ func add(a, b, c)") {
		t.Errorf("edit preview missing diff: %q", ae.Request.Preview)
	}

	// Approved execution applies the change.
	_, err = EditFileTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"path": "f.go", "old_str": "func add(a, b)", "new_str": "func add(a, b, c)"})
	if err != nil {
		t.Fatalf("approved edit: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "f.go"))
	if string(data) != "func add(a, b, c)" {
		t.Errorf("edit not applied: %q", data)
	}
}

func TestEditFile_OldStrNotFound(t *testing.T) {
	cfg, dir := localCfg(t)
	_ = os.WriteFile(filepath.Join(dir, "f.txt"), []byte("abc"), 0o600)
	_, err := EditFileTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"path": "f.txt", "old_str": "xyz", "new_str": "q"})
	if err == nil {
		t.Error("editing a missing old_str should error")
	}
}

func TestRunTerminal_RequiresApproval(t *testing.T) {
	cfg, _ := localCfg(t)
	_, err := RunTerminalTool{cfg: cfg, RequireApproval: true}.Execute(context.Background(),
		map[string]any{"command": "echo hi"})
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("run_terminal should require approval, got %v", err)
	}
	if !strings.Contains(ae.Request.Preview, "echo hi") {
		t.Errorf("command preview missing: %q", ae.Request.Preview)
	}
}

func TestRunTerminal_ExecutesAfterApproval(t *testing.T) {
	cfg, _ := localCfg(t)
	res, err := RunTerminalTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"command": "echo vortex"})
	if err != nil {
		t.Fatalf("approved run: %v", err)
	}
	m := res.(map[string]any)
	if !strings.Contains(m["stdout"].(string), "vortex") {
		t.Errorf("stdout = %q, want it to contain vortex", m["stdout"])
	}
	if m["exit_code"].(int) != 0 {
		t.Errorf("exit_code = %v, want 0", m["exit_code"])
	}
}

func TestRunTerminal_DangerousBlocked(t *testing.T) {
	cfg, _ := localCfg(t)
	for _, cmd := range []string{"rm -rf /", "rm -rf /*", ":(){ :|:& };:", "mkfs.ext4 /dev/sda"} {
		_, err := RunTerminalTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
			map[string]any{"command": cmd})
		if !errors.Is(err, ErrDangerousAction) {
			t.Errorf("command %q: err = %v, want ErrDangerousAction", cmd, err)
		}
	}
}

func TestCreateProject_RequiresApprovalThenCreates(t *testing.T) {
	cfg, dir := localCfg(t)
	// Approval gate.
	_, err := CreateProjectTool{cfg: cfg, RequireApproval: true}.Execute(context.Background(),
		map[string]any{"name": "calc", "type": "python"})
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("create_project should require approval, got %v", err)
	}
	if !strings.Contains(ae.Request.Preview, "main.py") {
		t.Errorf("project preview should list files: %q", ae.Request.Preview)
	}

	// Approved creation writes the files.
	res, err := CreateProjectTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"name": "calc", "type": "python"})
	if err != nil {
		t.Fatalf("approved create: %v", err)
	}
	_ = res
	if _, serr := os.Stat(filepath.Join(dir, "calc", "main.py")); serr != nil {
		t.Errorf("main.py not created: %v", serr)
	}
	if _, serr := os.Stat(filepath.Join(dir, "calc", "requirements.txt")); serr != nil {
		t.Errorf("requirements.txt not created: %v", serr)
	}
}

func TestCreateProject_GoSkeleton(t *testing.T) {
	cfg, dir := localCfg(t)
	_, err := CreateProjectTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"name": "svc", "type": "go"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "svc", "go.mod"))
	if !strings.Contains(string(data), "module svc") {
		t.Errorf("go.mod = %q", data)
	}
}

func TestNewLocalTools_AllApprovalGated(t *testing.T) {
	tools := NewLocalTools(LocalFSConfig{Root: t.TempDir()})
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name()] = true
	}
	for _, want := range []string{"list_directory", "read_file", "write_file", "edit_file", "run_terminal", "create_project"} {
		if !names[want] {
			t.Errorf("local tools missing %q", want)
		}
	}
	// Mutating tools must require approval (return ApprovalError).
	for _, tl := range tools {
		switch tl.Name() {
		case "write_file":
			_, err := tl.Execute(context.Background(), map[string]any{"path": "x", "content": "y"})
			assertApproval(t, "write_file", err)
		case "run_terminal":
			_, err := tl.Execute(context.Background(), map[string]any{"command": "echo x"})
			assertApproval(t, "run_terminal", err)
		}
	}
}

func assertApproval(t *testing.T, name string, err error) {
	t.Helper()
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Errorf("%s should be approval-gated, got %v", name, err)
	}
}
