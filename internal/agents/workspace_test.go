package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestContainInTrustedRoot_PrefixSiblingDenied(t *testing.T) {
	parent := t.TempDir()
	base := filepath.Join(parent, "tr")
	sibling := filepath.Join(parent, "trusted")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sibling, "secret.txt")
	if err := os.WriteFile(target, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := containInTrustedRoot(base, target); !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("prefix sibling must be denied: err=%v", err)
	}
}

func TestContainInTrustedRoot_EmptyTrustRootSkipsCheck(t *testing.T) {
	outside := t.TempDir()
	abs := filepath.Join(outside, "any.txt")
	got, err := containInTrustedRoot("", abs)
	if err != nil {
		t.Fatalf("empty trusted root: %v", err)
	}
	if got != abs {
		t.Fatalf("got %q, want %q", got, abs)
	}
}

func TestContainInTrustedRoot_NormalizesDotSegments(t *testing.T) {
	root := t.TempDir()
	inner := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(inner, "ok.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dotted := filepath.Join(root, "a", "b", "..", "b", "ok.txt")
	got, err := containInTrustedRoot(root, dotted)
	if err != nil {
		t.Fatalf("normalized in-scope path: %v", err)
	}
	if !pathsEquivalent(got, file) {
		t.Fatalf("resolved %q, want %q", got, file)
	}
}

func TestResolveLocal_EmptyTrustedRootAllowsAbsoluteOutside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	cfg := LocalFSConfig{Root: root} // no TrustedRoot
	abs := filepath.Join(outside, "remote.txt")
	if err := os.WriteFile(abs, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := cfg.resolveLocal(abs)
	if err != nil {
		t.Fatalf("without TrustedRoot absolute paths are allowed: %v", err)
	}
	if resolved != abs {
		t.Fatalf("resolved %q, want %q", resolved, abs)
	}
}

func TestResolveLocal_RelativeParentTraversalDenied(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "trusted")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := LocalFSConfig{Root: root, TrustedRoot: root}
	_, err := cfg.resolveLocal(filepath.Join("..", "outside", "secret.txt"))
	if !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf(".. traversal: err=%v, want ErrWorkspaceEscape", err)
	}
}

func TestResolveLocal_TrustedRootDeniesEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	cfg := LocalFSConfig{Root: root, TrustedRoot: root}

	if _, err := cfg.resolveLocal("ok.txt"); err != nil {
		t.Fatalf("in-scope relative: %v", err)
	}
	_, err := cfg.resolveLocal(filepath.Join(outside, "secret.txt"))
	if !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("out-of-scope absolute: err=%v, want ErrWorkspaceEscape", err)
	}
	_, err = cfg.resolveLocal(filepath.Join("..", filepath.Base(outside), "x"))
	if err == nil {
		// Depending on temp layout, .. may or may not escape; if Abs stays inside, skip.
		abs, aerr := filepath.Abs(filepath.Join(root, "..", filepath.Base(outside), "x"))
		if aerr == nil {
			if _, cerr := containInTrustedRoot(root, abs); cerr == nil {
				t.Skip("temp dirs share a parent that keeps path inside root layout")
			}
		}
	}
	if err != nil && !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("traversal: %v", err)
	}
}

func TestResolveLocal_SymlinkEscapeDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Creating symlinks often requires elevated privileges on Windows CI.
		t.Skip("symlink escape covered on unix; windows junction privileges vary")
	}
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not permitted: %v", err)
	}
	cfg := LocalFSConfig{Root: root, TrustedRoot: root}
	_, err := cfg.resolveLocal(filepath.Join("escape", "secret.txt"))
	if !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("symlink escape: err=%v, want ErrWorkspaceEscape", err)
	}
}

func TestWriteLocalFile_TrustedRootDeniesOutOfScope(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "x.txt")
	cfg := LocalFSConfig{Root: root, TrustedRoot: root}
	_, err := WriteLocalFileTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"path": outside, "content": "x"})
	if !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("err=%v, want ErrWorkspaceEscape", err)
	}
}

func TestWriteLocalFile_AutoModeWaivesInScopeApproval(t *testing.T) {
	root := t.TempDir()
	cfg := LocalFSConfig{Root: root, TrustedRoot: root, AutoMode: true}
	_, err := WriteLocalFileTool{cfg: cfg, RequireApproval: true}.Execute(context.Background(),
		map[string]any{"path": "in.txt", "content": "hi"})
	if err != nil {
		t.Fatalf("Auto Mode in-scope write: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "in.txt"))
	if string(data) != "hi" {
		t.Errorf("content=%q", data)
	}
}

// A hook written without approval would run on the next approved git commit, so
// Auto Mode must not waive these paths even though they are inside the root.
func TestWriteLocalFile_AutoModeKeepsApprovalForSensitivePaths(t *testing.T) {
	gated := []string{
		".git/hooks/pre-commit", ".git/config", ".GIT/hooks/pre-push", "sub/.git/config",
		".githooks/pre-commit", ".husky/pre-commit", "vortex.cue", "conf/VORTEX.CUE",
		".git./config", ".git /config", "git~1/config",
	}
	waived := []string{"src/hook.go", ".gitignore", ".github/notes.md", "git/config", "vortex.cue.bak"}
	root := t.TempDir()
	cfg := LocalFSConfig{Root: root, TrustedRoot: root, AutoMode: true}
	tool := WriteLocalFileTool{cfg: cfg, RequireApproval: true}
	write := func(p string) error {
		_, err := tool.Execute(context.Background(), map[string]any{
			"path": p, "content": "x", "create_dirs": true,
		})
		return err
	}
	for _, p := range gated {
		var ae *ApprovalError
		if err := write(p); !errors.As(err, &ae) {
			t.Errorf("%s: err = %v, want approval required", p, err)
		}
	}
	for _, p := range waived {
		if err := write(p); err != nil {
			t.Errorf("%s: err = %v, want written without approval", p, err)
		}
	}
}

// A link inside the root must not launder a write into .git.
func TestWriteLocalFile_AutoModeKeepsApprovalThroughSymlink(t *testing.T) {
	root := t.TempDir()
	hooks := filepath.Join(root, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(hooks, filepath.Join(root, "innocent")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg := LocalFSConfig{Root: root, TrustedRoot: root, AutoMode: true}
	_, err := WriteLocalFileTool{cfg: cfg, RequireApproval: true}.Execute(context.Background(),
		map[string]any{"path": "innocent/pre-commit", "content": "x"})
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want approval required", err)
	}
}

func TestWriteLocalFile_AutoModeStillRequiresApprovalWithoutTrust(t *testing.T) {
	root := t.TempDir()
	cfg := LocalFSConfig{Root: root, AutoMode: true} // no TrustedRoot
	_, err := WriteLocalFileTool{cfg: cfg, RequireApproval: true}.Execute(context.Background(),
		map[string]any{"path": "in.txt", "content": "hi"})
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("Auto Mode without TrustedRoot must still require approval, got %v", err)
	}
}

func TestRunTerminal_AutoModeDoesNotWaiveShellApproval(t *testing.T) {
	root := t.TempDir()
	cfg := LocalFSConfig{Root: root, TrustedRoot: root, AutoMode: true}
	_, err := RunTerminalTool{cfg: cfg, RequireApproval: true}.Execute(context.Background(),
		map[string]any{"command": "echo hi"})
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("shell must remain approval-gated under Auto Mode, got %v", err)
	}
}

func TestRunTerminal_EmptyCwdFallsBackInsideTrustedRoot(t *testing.T) {
	trusted := t.TempDir()
	outside := t.TempDir()
	cfg := LocalFSConfig{Root: outside, TrustedRoot: trusted}
	tool := RunTerminalTool{cfg: cfg, approved: true}
	if _, cerr := containInTrustedRoot(trusted, outside); cerr == nil {
		t.Fatal("test setup: outside must not be inside trusted")
	}
	wantCwd, err := containInTrustedRoot(trusted, trusted)
	if err != nil {
		t.Fatalf("trusted root self-resolve: %v", err)
	}
	// Print the working directory with the platform shell: `cd` (cmd.exe) prints
	// the cwd on Windows, while POSIX `cd` prints nothing — use `pwd -P` there
	// (physical path, matching the symlink-resolved trusted root).
	printCwd := "pwd -P"
	if runtime.GOOS == "windows" {
		printCwd = "cd"
	}
	res, err := tool.Execute(context.Background(), map[string]any{"command": printCwd})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok || m["exit_code"] != 0 {
		t.Fatalf("result = %#v", res)
	}
	stdout := strings.TrimSpace(m["stdout"].(string))
	if !pathsEquivalent(stdout, wantCwd) {
		t.Fatalf("empty cwd fallback: stdout cwd %q, want trusted %q", stdout, wantCwd)
	}
}

// TestPathWithinBase_Table pins the lexical containment semantics that
// containInTrustedRoot relies on, including the platform-specific behaviour
// of filepath.Rel (case folding and volume boundaries on Windows).
func TestPathWithinBase_Table(t *testing.T) {
	// A synthetic absolute root so cases do not depend on temp-dir layout.
	root := string(os.PathSeparator) + "trusted"
	if runtime.GOOS == "windows" {
		root = `C:\trusted`
	}
	sep := string(os.PathSeparator)
	type pwbCase struct {
		name string
		path string
		base string
		want bool
	}
	cases := []pwbCase{
		{"same path", root, root, true},
		{"direct child", root + sep + "x", root, true},
		{"nested child", root + sep + "a" + sep + "b.txt", root, true},
		{"trailing separator on path", root + sep, root, true},
		{"trailing separator on base", root, root + sep, true},
		{"dot segment", root + sep + ".", root, true},
		{"dot-dot that stays inside", root + sep + "a" + sep + ".." + sep + "b", root, true},
		{"child literally named ..foo", root + sep + "..foo", root, true},
		{"parent via ..", root + sep + "..", root, false},
		{"parent directory", filepath.Dir(root), root, false},
		{"dot-dot escaping", root + sep + ".." + sep + "other", root, false},
		{"sibling prefix trap (longer)", root + "X" + sep + "f", root, false},
		{"sibling prefix trap (shorter)", filepath.Dir(root) + sep + "tr", root, false},
		{"unrelated sibling", filepath.Dir(root) + sep + "other" + sep + "f", root, false},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, []pwbCase{
			{"win: case-insensitive volume+dir", `c:\TRUSTED\x`, `C:\trusted`, true},
			{"win: case-insensitive base only", `C:\trusted\x`, `c:\Trusted`, true},
			{"win: forward slashes", `C:/trusted/x`, `C:\trusted`, true},
			{"win: different volume denied", `D:\trusted\x`, `C:\trusted`, false},
			{"win: drive root base, same volume", `C:\x`, `C:\`, true},
			{"win: drive root base, other volume", `D:\x`, `C:\`, false},
			{"win: drive-relative path denied", `C:trusted\x`, `C:\trusted`, false},
			{"win: extended-length prefix is a different volume", `\\?\C:\trusted\x`, `C:\trusted`, false},
			{"win: UNC same share", `\\srv\share\dir\sub`, `\\srv\share\dir`, true},
			{"win: UNC same share, case-folded", `\\SRV\Share\DIR\sub`, `\\srv\share\dir`, true},
			{"win: UNC different share denied", `\\srv\second\dir\sub`, `\\srv\share\dir`, false},
			{"win: UNC sibling prefix trap", `\\srv\share\dirX\sub`, `\\srv\share\dir`, false},
		}...)
	} else {
		cases = append(cases, pwbCase{"posix: case-sensitive", "/TRUSTED/x", root, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathWithinBase(tc.path, tc.base); got != tc.want {
				t.Fatalf("pathWithinBase(%q, %q) = %v, want %v", tc.path, tc.base, got, tc.want)
			}
		})
	}
}

func TestContainInTrustedRoot_WindowsCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case folding is a Windows filesystem property")
	}
	root := t.TempDir()
	file := filepath.Join(root, "ok.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Same file addressed with different letter case must still be inside.
	flipped := strings.ToUpper(root) + `\OK.TXT`
	got, err := containInTrustedRoot(root, flipped)
	if err != nil {
		t.Fatalf("case-flipped in-scope path: %v", err)
	}
	if !pathsEquivalent(got, file) {
		t.Fatalf("resolved %q, want %q", got, file)
	}
	// And the trusted root itself may be given in different case.
	if _, err := containInTrustedRoot(strings.ToLower(root), file); err != nil {
		t.Fatalf("lower-cased trusted root: %v", err)
	}
}

func TestContainInTrustedRoot_TrailingSeparatorAndDotPaths(t *testing.T) {
	root := t.TempDir()
	sep := string(os.PathSeparator)
	// Trailing separator on the trusted root is cleaned away.
	if _, err := containInTrustedRoot(root+sep, filepath.Join(root, "x.txt")); err != nil {
		t.Fatalf("trailing separator root: %v", err)
	}
	// Trailing separator / "." on the target resolve to the root itself.
	for _, p := range []string{root + sep, root + sep + "."} {
		got, err := containInTrustedRoot(root, p)
		if err != nil {
			t.Fatalf("%q: %v", p, err)
		}
		if !pathsEquivalent(got, root) {
			t.Fatalf("%q resolved to %q, want root", p, got)
		}
	}
	// ".." alone and "../x" escape.
	for _, p := range []string{root + sep + "..", filepath.Join(root, "..", "x")} {
		if _, err := containInTrustedRoot(root, p); !errors.Is(err, ErrWorkspaceEscape) {
			t.Fatalf("%q: err=%v, want ErrWorkspaceEscape", p, err)
		}
	}
	// Via resolveLocal: "" and "." are the root; ".." escapes when Root ==
	// TrustedRoot.
	cfg := LocalFSConfig{Root: root, TrustedRoot: root}
	for _, p := range []string{"", "."} {
		got, err := cfg.resolveLocal(p)
		if err != nil {
			t.Fatalf("resolveLocal(%q): %v", p, err)
		}
		if !pathsEquivalent(got, root) {
			t.Fatalf("resolveLocal(%q) = %q, want root", p, got)
		}
	}
	if _, err := cfg.resolveLocal(".."); !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("resolveLocal(..): err=%v, want ErrWorkspaceEscape", err)
	}
}

func TestContainInTrustedRoot_NonExistentTargetUnderSymlinkedAncestorDenied(t *testing.T) {
	// Regression: only the immediate parent used to be symlink-checked, so a
	// not-yet-existing intermediate directory under an escaping symlink
	// slipped through (and create_dirs would then MkdirAll through the link).
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not permitted: %v", err)
	}
	target := filepath.Join(link, "newdir", "deeper", "f.txt") // newdir does not exist
	if _, err := containInTrustedRoot(root, target); !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("ancestor symlink escape: err=%v, want ErrWorkspaceEscape", err)
	}
	// End-to-end through write_file with create_dirs (the exploitable path).
	cfg := LocalFSConfig{Root: root, TrustedRoot: root}
	_, err := WriteLocalFileTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"path": filepath.Join("escape", "newdir", "deeper", "f.txt"), "content": "x", "create_dirs": true})
	if !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("write_file through symlinked ancestor: err=%v, want ErrWorkspaceEscape", err)
	}
	if _, serr := os.Stat(filepath.Join(outside, "newdir")); serr == nil {
		t.Fatal("write_file created a directory outside the trusted root")
	}
	// A symlink that stays INSIDE the root remains usable.
	inner := filepath.Join(root, "real")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	innerLink := filepath.Join(root, "alias")
	if err := os.Symlink(inner, innerLink); err != nil {
		t.Skipf("symlink not permitted: %v", err)
	}
	if _, err := containInTrustedRoot(root, filepath.Join(innerLink, "newdir", "f.txt")); err != nil {
		t.Fatalf("in-root symlinked ancestor must be allowed: %v", err)
	}
}

func TestContainInTrustedRoot_NonExistentDeepPathInsideRootAllowed(t *testing.T) {
	// No symlinks at all: several missing directory levels under the root are
	// fine (the ancestor walk must stop at the root, not deny).
	root := t.TempDir()
	target := filepath.Join(root, "a", "b", "c", "new.txt")
	got, err := containInTrustedRoot(root, target)
	if err != nil {
		t.Fatalf("deep non-existent in-root path: %v", err)
	}
	if !pathsEquivalent(got, target) {
		t.Fatalf("resolved %q, want %q", got, target)
	}
}

func TestRunTerminal_CwdOutsideTrustedRootDenied(t *testing.T) {
	trusted := t.TempDir()
	outside := t.TempDir()
	cfg := LocalFSConfig{Root: trusted, TrustedRoot: trusted}
	// Even an already-approved action must not run outside the workspace.
	tool := RunTerminalTool{cfg: cfg, approved: true}
	_, err := tool.Execute(context.Background(), map[string]any{"command": "echo hi", "cwd": outside})
	if !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("cwd outside trusted root: err=%v, want ErrWorkspaceEscape", err)
	}
	_, err = tool.Execute(context.Background(), map[string]any{"command": "echo hi", "cwd": ".."})
	if !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("relative cwd escaping trusted root: err=%v, want ErrWorkspaceEscape", err)
	}
}

func TestRunTerminal_ApprovalGateHonoursBuildMode(t *testing.T) {
	// Dev/test builds: RequireApproval=false with no approval executes.
	// Release builds (-tags=release): approvalAlwaysRequired makes the field
	// irrelevant — only the unexported `approved` flag set by the approval
	// flow may run a command (audit I6). This test is meaningful under both
	// build modes and documents the contract it asserts.
	cfg := LocalFSConfig{Root: t.TempDir()}
	_, err := RunTerminalTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"command": "echo gate"})
	var ae *ApprovalError
	if approvalAlwaysRequired {
		if !errors.As(err, &ae) {
			t.Fatalf("release build: RequireApproval=false must still gate, got %v", err)
		}
	} else if err != nil {
		t.Fatalf("dev build: RequireApproval=false should execute, got %v", err)
	}
	// With approved set, both build modes execute.
	if _, err := (RunTerminalTool{cfg: cfg, approved: true}).Execute(context.Background(),
		map[string]any{"command": "echo gate"}); err != nil {
		t.Fatalf("approved command: %v", err)
	}
}

func pathsEquivalent(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if a == b {
		return true
	}
	aa, aerr := filepath.Abs(a)
	bb, berr := filepath.Abs(b)
	if aerr == nil && berr == nil && aa == bb {
		return true
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(aa, bb)
	}
	return false
}
