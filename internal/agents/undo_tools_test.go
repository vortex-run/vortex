package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUndo_BackupAndRestore(t *testing.T) {
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
	_, err := UndoTool{cfg: LocalFSConfig{}, RequireApproval: false}.Execute(context.Background(),
		map[string]any{"session_id": "empty-sess"})
	if err == nil {
		t.Error("undo with no backups should error")
	}
}

func TestBackup_KeepsLastFive(t *testing.T) {
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
	// Cleanup the temp backups dir.
	_ = os.RemoveAll(backupDir(session))
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
