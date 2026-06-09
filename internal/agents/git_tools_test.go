package agents

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a temp git repo with an initial commit and returns its dir.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func TestGitStatus_CleanAndDirty(t *testing.T) {
	dir := initRepo(t)
	cfg := LocalFSConfig{Root: dir}

	// Clean tree.
	res, err := GitStatusTool{cfg: cfg}.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if !m["clean"].(bool) {
		t.Errorf("fresh repo should be clean: %+v", m)
	}

	// Add an untracked file → dirty.
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("x"), 0o600)
	res, _ = GitStatusTool{cfg: cfg}.Execute(context.Background(), nil)
	m = res.(map[string]any)
	if m["clean"].(bool) {
		t.Error("repo with an untracked file should be dirty")
	}
	if untr := m["untracked"].([]string); len(untr) != 1 || untr[0] != "b.txt" {
		t.Errorf("untracked = %v, want [b.txt]", m["untracked"])
	}
}

func TestGitDiff_ShowsChanges(t *testing.T) {
	dir := initRepo(t)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o600)
	res, err := GitDiffTool{cfg: LocalFSConfig{Root: dir}}.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if d := res.(map[string]any)["diff"].(string); !strings.Contains(d, "+two") {
		t.Errorf("diff should show the added line, got:\n%s", d)
	}
}

func TestGitAdd_RequiresApprovalThenStages(t *testing.T) {
	dir := initRepo(t)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("x"), 0o600)
	cfg := LocalFSConfig{Root: dir}

	_, err := GitAddTool{cfg: cfg, RequireApproval: true}.Execute(context.Background(),
		map[string]any{"files": []string{"b.txt"}})
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("git_add should require approval, got %v", err)
	}

	_, err = GitAddTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"files": []string{"b.txt"}})
	if err != nil {
		t.Fatalf("approved git_add: %v", err)
	}
	// b.txt should now be staged.
	res, _ := GitStatusTool{cfg: cfg}.Execute(context.Background(), nil)
	if staged := res.(map[string]any)["staged"].([]string); len(staged) != 1 || staged[0] != "b.txt" {
		t.Errorf("staged = %v, want [b.txt]", res.(map[string]any)["staged"])
	}
}

func TestGitCommit_RequiresApprovalThenCommits(t *testing.T) {
	dir := initRepo(t)
	_ = os.WriteFile(filepath.Join(dir, "c.txt"), []byte("y"), 0o600)
	cfg := LocalFSConfig{Root: dir}

	_, err := GitCommitTool{cfg: cfg, RequireApproval: true}.Execute(context.Background(),
		map[string]any{"message": "add c", "files": []string{"c.txt"}})
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("git_commit should require approval, got %v", err)
	}
	if !strings.Contains(ae.Request.Preview, "add c") {
		t.Errorf("commit preview should show the message: %q", ae.Request.Preview)
	}

	_, err = GitCommitTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"message": "add c", "files": []string{"c.txt"}})
	if err != nil {
		t.Fatalf("approved git_commit: %v", err)
	}
	// Working tree should be clean after the commit.
	res, _ := GitStatusTool{cfg: cfg}.Execute(context.Background(), nil)
	if !res.(map[string]any)["clean"].(bool) {
		t.Error("tree should be clean after committing the only change")
	}
}

func TestGitTools_RegisteredInLocalTools(t *testing.T) {
	names := map[string]bool{}
	for _, tl := range NewLocalTools(LocalFSConfig{Root: t.TempDir()}) {
		names[tl.Name()] = true
	}
	for _, want := range []string{"git_status", "git_diff", "git_add", "git_commit"} {
		if !names[want] {
			t.Errorf("local tools missing %q", want)
		}
	}
}

func TestRuleClassify_GitOps(t *testing.T) {
	for _, msg := range []string{"git status", "what changed", "show changes", "git diff", "commit my work", "stage files"} {
		if got := ruleClassify(msg); got != IntentLocalFile {
			t.Errorf("ruleClassify(%q) = %q, want LOCAL_FILE", msg, got)
		}
	}
}

func TestParseLocalRequest_Git(t *testing.T) {
	if tool, _ := parseLocalRequest("git status"); tool != "git_status" {
		t.Errorf("git status → %q", tool)
	}
	if tool, _ := parseLocalRequest("/diff a.txt"); tool != "git_diff" {
		t.Errorf("/diff → %q", tool)
	}
	tool, params := parseLocalRequest("/commit fix the bug")
	if tool != "git_commit" || params["message"] != "fix the bug" {
		t.Errorf("/commit → %q %v", tool, params)
	}
}
