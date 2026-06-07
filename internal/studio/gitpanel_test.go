package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a temp git repo with one commit and returns its path. It
// skips the test if git is unavailable.
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
	run("init", "-b", "main")
	run("config", "user.email", "test@vortex.local")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "initial commit")
	return dir
}

func gitReq(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestNewGitPanel_NonRepoErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := NewGitPanel(GitPanelConfig{RepoPath: t.TempDir(), Logger: discardLogger()}); err == nil {
		t.Error("expected error for a non-git directory")
	}
}

func TestGitPanel_Status(t *testing.T) {
	dir := initRepo(t)
	gp, err := NewGitPanel(GitPanelConfig{RepoPath: dir, Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	rec := gitReq(t, gp.Handler(), http.MethodGet, "/studio/git/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var resp struct {
		Branch string `json:"branch"`
		Clean  bool   `json:"clean"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Branch != "main" {
		t.Errorf("branch = %q, want main", resp.Branch)
	}
	if !resp.Clean {
		t.Error("fresh repo should be clean")
	}
}

func TestGitPanel_StatusDetectsModified(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gp, _ := NewGitPanel(GitPanelConfig{RepoPath: dir, Logger: discardLogger()})
	rec := gitReq(t, gp.Handler(), http.MethodGet, "/studio/git/status", "")
	var resp struct {
		Clean    bool     `json:"clean"`
		Unstaged []string `json:"unstaged"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Clean {
		t.Error("modified repo should not be clean")
	}
	if len(resp.Unstaged) == 0 {
		t.Error("expected README.md in unstaged")
	}
}

func TestGitPanel_Log(t *testing.T) {
	dir := initRepo(t)
	gp, _ := NewGitPanel(GitPanelConfig{RepoPath: dir, Logger: discardLogger()})
	rec := gitReq(t, gp.Handler(), http.MethodGet, "/studio/git/log?limit=5", "")
	var resp struct {
		Commits []struct {
			Hash    string `json:"hash"`
			Message string `json:"message"`
		} `json:"commits"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Commits) != 1 || resp.Commits[0].Message != "initial commit" {
		t.Errorf("log = %+v, want one 'initial commit'", resp.Commits)
	}
}

func TestGitPanel_Diff(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gp, _ := NewGitPanel(GitPanelConfig{RepoPath: dir, Logger: discardLogger()})
	rec := gitReq(t, gp.Handler(), http.MethodGet, "/studio/git/diff?file=README.md", "")
	var resp struct {
		Diff string `json:"diff"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !strings.Contains(resp.Diff, "changed") {
		t.Errorf("diff should mention the change: %q", resp.Diff)
	}
}

func TestGitPanel_Commit(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &recordingAudit{}
	gp, _ := NewGitPanel(GitPanelConfig{RepoPath: dir, Logger: discardLogger(), AuditLog: rec})

	resp := gitReq(t, gp.Handler(), http.MethodPost, "/studio/git/commit",
		`{"message":"add new file","files":["new.txt"]}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("commit = %d, want 200 (%s)", resp.Code, resp.Body)
	}
	if !rec.has("studio.git.commit") {
		t.Error("commit should be audit-logged")
	}
	// Verify the commit exists.
	logRec := gitReq(t, gp.Handler(), http.MethodGet, "/studio/git/log?limit=5", "")
	if !strings.Contains(logRec.Body.String(), "add new file") {
		t.Error("new commit should appear in the log")
	}
}

func TestGitPanel_CommitRequiresMessage(t *testing.T) {
	dir := initRepo(t)
	gp, _ := NewGitPanel(GitPanelConfig{RepoPath: dir, Logger: discardLogger()})
	rec := gitReq(t, gp.Handler(), http.MethodPost, "/studio/git/commit", `{"message":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty message = %d, want 400", rec.Code)
	}
}

func TestGitPanel_Push(t *testing.T) {
	dir := initRepo(t)
	// Create a bare remote and wire it as origin.
	remote := t.TempDir()
	runGit(t, remote, "init", "--bare", "-b", "main")
	runGit(t, dir, "remote", "add", "origin", remote)

	rec := &recordingAudit{}
	gp, _ := NewGitPanel(GitPanelConfig{RepoPath: dir, Logger: discardLogger(), AuditLog: rec})
	resp := gitReq(t, gp.Handler(), http.MethodPost, "/studio/git/push", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("push = %d (%s)", resp.Code, resp.Body)
	}
	var body struct {
		Success bool `json:"success"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &body)
	if !body.Success {
		t.Errorf("push to bare remote should succeed: %s", resp.Body)
	}
	if !rec.has("studio.git.push") {
		t.Error("push should be audit-logged")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
