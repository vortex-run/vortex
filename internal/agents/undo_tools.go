package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vortex-run/vortex/internal/userdir"
)

// Undo support: before write_file overwrites an existing file, a backup is
// written under <user cache dir>/vortex/backups/<session>/, keeping the last
// 5. The undo tool restores the most recent backup (approval-gated).
//
// The backups used to live in os.TempDir()/vortex-backups, a fixed path in a
// directory every local user can write on Linux and macOS: another user could
// create it first and plant a backup whose metadata names any file the
// Gubern user can write (an undo then overwrites it), or symlink it elsewhere.
// The session ID was also used as a path element unchecked ("../.." escaped).

// maxBackupsPerSession bounds retained backups per session.
const maxBackupsPerSession = 5

// backupMeta records where a backup came from.
type backupMeta struct {
	OriginalPath string    `json:"original_path"`
	CreatedAt    time.Time `json:"created_at"`
}

// backupRoot returns the per-user directory that holds every session's
// backups. A variable so tests can point it at a temp dir.
var backupRoot = defaultBackupRoot

// defaultBackupRoot is <user cache dir>/vortex/backups.
func defaultBackupRoot() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userdir.App(dir), "backups"), nil
}

// safeBackupSession matches session IDs usable as a directory name as-is.
var safeBackupSession = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// backupDir returns the backup directory for a session, or "" when there is
// no private place to keep backups (then nothing is backed up or undone).
func backupDir(session string) string {
	if session == "" {
		session = "default"
	}
	if !safeBackupSession.MatchString(session) {
		session = "s-" + hashPath(session)
	}
	root, err := backupRoot()
	if err != nil || root == "" {
		return ""
	}
	return filepath.Join(root, session)
}

// backupBeforeWrite saves a copy of path (if it exists) under the session's
// backup dir, pruning to the most recent maxBackupsPerSession. Best-effort: a
// backup failure must not block the write.
func backupBeforeWrite(session, path string) {
	data, err := os.ReadFile(path) //nolint:gosec // backing up a user file
	if err != nil {
		return // file doesn't exist yet → nothing to back up
	}
	dir := backupDir(session)
	if dir == "" {
		return
	}
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return
	}
	stamp := time.Now().Format("20060102-150405.000")
	base := stamp + "_" + hashPath(path)
	if werr := os.WriteFile(filepath.Join(dir, base+".bak"), data, 0o600); werr != nil {
		return
	}
	meta, _ := json.Marshal(backupMeta{OriginalPath: path, CreatedAt: time.Now()})
	_ = os.WriteFile(filepath.Join(dir, base+".json"), meta, 0o600)
	pruneBackups(dir)
}

// hashPath returns a short stable hash of a path for the backup filename.
func hashPath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:8]
}

// pruneBackups keeps only the most recent maxBackupsPerSession backup pairs.
func pruneBackups(dir string) {
	baks := listBackups(dir)
	for i := maxBackupsPerSession; i < len(baks); i++ {
		_ = os.Remove(baks[i].bakPath)
		_ = os.Remove(strings.TrimSuffix(baks[i].bakPath, ".bak") + ".json")
	}
}

// backupEntry is one stored backup, newest first.
type backupEntry struct {
	bakPath string
	meta    backupMeta
}

// listBackups returns backups in a dir, newest first.
func listBackups(dir string) []backupEntry {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []backupEntry
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".bak") {
			continue
		}
		bakPath := filepath.Join(dir, e.Name())
		var meta backupMeta
		if data, rerr := os.ReadFile(strings.TrimSuffix(bakPath, ".bak") + ".json"); rerr == nil {
			_ = json.Unmarshal(data, &meta)
		}
		out = append(out, backupEntry{bakPath: bakPath, meta: meta})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].meta.CreatedAt.After(out[j].meta.CreatedAt) })
	return out
}

// UndoTool restores the most recent file backup for a session. Requires
// approval (it overwrites the current file).
type UndoTool struct {
	cfg             LocalFSConfig
	RequireApproval bool
}

// Name returns the tool name.
func (UndoTool) Name() string { return "undo" }

// SideEffecting marks undo (a file restore) for effect fencing (H3 increment 2).
func (UndoTool) SideEffecting() bool { return true }

// Description returns a human-readable summary.
func (UndoTool) Description() string { return "Undo the last file write (approval required)" }

// Execute restores the most recent backup for params["session_id"].
func (t UndoTool) Execute(_ context.Context, params map[string]any) (any, error) {
	session, _ := params["session_id"].(string)
	baks := listBackups(backupDir(session))
	if len(baks) == 0 {
		return nil, fmt.Errorf("agents: nothing to undo")
	}
	latest := baks[0]
	if t.RequireApproval {
		preview := "Restore " + latest.meta.OriginalPath + " to its previous contents"
		return nil, &ApprovalError{Request: ApprovalRequest{
			Tool:        t.Name(),
			Description: "undo last write to " + latest.meta.OriginalPath,
			Preview:     preview,
			Params:      params,
		}}
	}
	data, err := os.ReadFile(latest.bakPath) //nolint:gosec // session-local backup
	if err != nil {
		return nil, err
	}
	if werr := os.WriteFile(latest.meta.OriginalPath, data, 0o644); werr != nil { //nolint:gosec // restore
		return nil, werr
	}
	_ = os.Remove(latest.bakPath)
	_ = os.Remove(strings.TrimSuffix(latest.bakPath, ".bak") + ".json")
	return map[string]any{"restored": latest.meta.OriginalPath, "bytes": len(data)}, nil
}
