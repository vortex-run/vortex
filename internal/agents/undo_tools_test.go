package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useBackupRoot points undo backups at a per-test directory.
func useBackupRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	prev := backupRoot
	backupRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { backupRoot = prev })
	return root
}

// TestBackupDir_PrivateAndConfined: a session ID can't climb out of the
// per-user backup root, and with no private root backups are disabled
// rather than falling back to the shared temp dir.
func TestBackupDir_PrivateAndConfined(t *testing.T) {
	root := useBackupRoot(t)
	sessions := []string{"sess-1", "", "../../etc", `..\..\x`, "a/b", strings.Repeat("x", 200)}
	for _, session := range sessions {
		dir := backupDir(session)
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.ContainsAny(rel, `/\`) {
			t.Errorf("backupDir(%q) = %q, want exactly one level under %q", session, dir, root)
		}
	}

	backupRoot = func() (string, error) { return "", os.ErrNotExist }
	if dir := backupDir("x"); dir != "" {
		t.Errorf("with no private root, backupDir = %q, want empty (backups disabled)", dir)
	}
}

// TestBackupRoot_NotSharedTemp: the default root is the per-user cache dir.
func TestBackupRoot_NotSharedTemp(t *testing.T) {
	root, err := defaultBackupRoot()
	if err != nil {
		t.Skipf("no user cache dir here: %v", err)
	}
	cache, _ := os.UserCacheDir()
	if !strings.HasPrefix(root, cache) {
		t.Errorf("backupRoot = %q, want under the user cache dir %q", root, cache)
	}
}

func TestUndo_BackupAndRestore(t *testing.T) {
	useBackupRoot(t)
	dir := t.TempDir()
	cfg := LocalFSConfig{Root: dir}
	target := filepath.Join(dir, "f.txt")
	session := "undo-sess-1"

	// Initial write (no backup — file didn't exist), then overwrite (backs up).
	w := WriteLocalFileTool{cfg: cfg, RequireApproval: false}
	_, _ = w.Execute(context.Background(), map[string]any{"path": target, "content": "v1", "session_id": session})
	_, _ = w.Execute(context.Background(), map[string]any{"path": target, "content": "v2", "session_id": session})

	if data, _ := os.ReadFile(target); string(data) != "v2" {
		t.Fatalf("file should be v2, got %q", data)
	}

	// Undo requires approval.
	_, err := UndoTool{cfg: cfg, RequireApproval: true}.Execute(context.Background(),
		map[string]any{"session_id": session})
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("undo should require approval, got %v", err)
	}

	// Approved undo restores v1.
	res, err := UndoTool{cfg: cfg, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"session_id": session})
	if err != nil {
		t.Fatalf("approved undo: %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "v1" {
		t.Errorf("undo should restore v1, got %q", data)
	}
	if res.(map[string]any)["restored"].(string) != target {
		t.Errorf("restored path = %v, want %s", res.(map[string]any)["restored"], target)
	}
}

func TestUndo_NothingToUndo(t *testing.T) {
	useBackupRoot(t)
	_, err := UndoTool{cfg: LocalFSConfig{}, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"session_id": "empty-sess"})
	if err == nil {
		t.Error("undo with no backups should error")
	}
}

func TestBackup_KeepsLastFive(t *testing.T) {
	useBackupRoot(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	session := "prune-sess"
	_ = os.WriteFile(target, []byte("seed"), 0o600)
	// 7 overwrites → 7 backups attempted, pruned to 5.
	for i := 0; i < 7; i++ {
		backupBeforeWrite(session, target)
		_ = os.WriteFile(target, []byte("v"), 0o600)
	}
	baks := listBackups(backupDir(session))
	if len(baks) > maxBackupsPerSession {
		t.Errorf("kept %d backups, want <= %d", len(baks), maxBackupsPerSession)
	}
}

func TestUndo_RegisteredAndApprovalGated(t *testing.T) {
	var found bool
	for _, tl := range NewLocalTools(LocalFSConfig{Root: t.TempDir()}) {
		if tl.Name() == "undo" {
			found = true
		}
	}
	if !found {
		t.Error("undo tool should be registered")
	}
}

// TestUndo_RefusesATargetOutsideTheWorkspace: a backup whose metadata names a
// file outside the trusted root is not restored there.
func TestUndo_RefusesATargetOutsideTheWorkspace(t *testing.T) {
	useBackupRoot(t)
	workspace, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := "undo-escape"
	backupBeforeWrite(session, victim) // as if planted: names a file outside
	if err := os.WriteFile(victim, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := LocalFSConfig{Root: workspace, TrustedRoot: workspace}
	for _, gated := range []bool{true, false} {
		_, err := UndoTool{cfg: cfg, RequireApproval: gated}.Execute(context.Background(), map[string]any{"session_id": session})
		if !errors.Is(err, ErrWorkspaceEscape) {
			t.Errorf("gated=%v: err = %v, want ErrWorkspaceEscape", gated, err)
		}
	}
	if data, _ := os.ReadFile(victim); string(data) != "current" {
		t.Errorf("file outside the workspace was overwritten: %q", data)
	}
}
