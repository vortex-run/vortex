package audit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rewriteEntry applies edit to entry seq (1-based) in the log file, keeping
// its stored hash — what an attacker without the key can do.
func rewriteEntry(t *testing.T, path string, seq int, edit func(*Entry)) {
	t.Helper()
	lines := readLines(t, path)
	var e Entry
	if err := json.Unmarshal([]byte(lines[seq-1]), &e); err != nil {
		t.Fatal(err)
	}
	edit(&e)
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	lines[seq-1] = string(b)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAudit_NewEntriesAreV2(t *testing.T) {
	l, path := newTestLog(t)
	if err := l.Append(context.Background(), "a", "tool.execute", "run_terminal", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readLines(t, path)[0], `"v":2`) {
		t.Fatalf("new entry is not v2: %s", readLines(t, path)[0])
	}
}

// Editing Detail (e.g. turning a failed command into a successful one) must
// break the chain. Before v2, Detail was outside the HMAC.
func TestAudit_VerifyDetectsDetailEdit(t *testing.T) {
	l, path := newTestLog(t)
	ctx := context.Background()
	_ = l.Append(ctx, "agent", "tool.execute", "run_terminal", map[string]any{"ok": false, "error": "exit status 1"})
	_ = l.Append(ctx, "studio", "db.query", "main", map[string]any{"query": "SELECT 1"})
	if err := l.Verify(); err != nil {
		t.Fatalf("untampered log fails: %v", err)
	}
	rewriteEntry(t, path, 1, func(e *Entry) { e.Detail["ok"] = true; delete(e.Detail, "error") })
	if err := l.Verify(); err == nil || !strings.Contains(err.Error(), "entry 1") {
		t.Fatalf("edited outcome not detected: %v", err)
	}
}

func TestAudit_VerifyDetectsDetailEditPastEntryOne(t *testing.T) {
	l, path := newTestLog(t)
	ctx := context.Background()
	_ = l.Append(ctx, "agent", "a", "r", nil)
	_ = l.Append(ctx, "studio", "db.query", "main", map[string]any{"query": "DROP TABLE users"})
	rewriteEntry(t, path, 2, func(e *Entry) { e.Detail["query"] = "SELECT 1" })
	if err := l.Verify(); err == nil || !strings.Contains(err.Error(), "entry 2") {
		t.Fatalf("rewritten query not detected: %v", err)
	}
}

// Stripping the version (to fall back to the detail-less v1 formula) must not
// let an edited detail through.
func TestAudit_VerifyDetectsVersionDowngrade(t *testing.T) {
	l, path := newTestLog(t)
	_ = l.Append(context.Background(), "agent", "tool.execute", "x", map[string]any{"ok": false})
	rewriteEntry(t, path, 1, func(e *Entry) { e.V = 0; e.Detail["ok"] = true })
	if err := l.Verify(); err == nil {
		t.Fatal("version downgrade + detail edit not detected")
	}
}

func TestAudit_UnknownVersionFails(t *testing.T) {
	l, path := newTestLog(t)
	_ = l.Append(context.Background(), "agent", "a", "r", nil)
	rewriteEntry(t, path, 1, func(e *Entry) { e.V = 9 })
	if err := l.Verify(); err == nil || !strings.Contains(err.Error(), "unknown hash version") {
		t.Fatalf("unknown version accepted: %v", err)
	}
}

// writeV1Log writes entries exactly as the pre-v2 code did (no "v" field,
// hash over the fields without Detail).
func writeV1Log(t *testing.T, path string, key []byte, n int) {
	t.Helper()
	l := &Log{path: path, key: key}
	var b strings.Builder
	prev := ""
	for i := 1; i <= n; i++ {
		e := Entry{Seq: uint64(i), Timestamp: time.Date(2026, 6, 1, 0, 0, i, 0, time.UTC),
			Actor: "system", Action: "legacy", Resource: "r", Detail: map[string]any{"n": i}}
		e.Hash = l.computeHash(prev, e.Seq, e.Timestamp, e.Actor, e.Action, e.Resource)
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
		prev = e.Hash
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Logs written before v2 keep verifying, and new v2 entries chain onto them.
func TestAudit_LegacyV1LogStillVerifiesAndExtends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	key := []byte("test-hmac-key")
	writeV1Log(t, path, key, 3)
	if strings.Contains(readLines(t, path)[0], `"v"`) {
		t.Fatal("test setup: legacy entry has a version field")
	}
	l, err := NewLog(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Verify(); err != nil {
		t.Fatalf("legacy v1 log no longer verifies: %v", err)
	}
	if err := l.Append(context.Background(), "agent", "tool.execute", "x", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	if err := l.Verify(); err != nil {
		t.Fatalf("mixed v1+v2 log fails: %v", err)
	}
	// The v2 entry protects its detail even after legacy ones.
	rewriteEntry(t, path, 4, func(e *Entry) { e.Detail["ok"] = false })
	if err := l.Verify(); err == nil || !strings.Contains(err.Error(), "entry 4") {
		t.Fatalf("detail edit on the v2 tail not detected: %v", err)
	}
}

type detailStruct struct {
	Zeta  string `json:"zeta"`
	Alpha int    `json:"alpha"`
}

// Detail that changes shape on a JSON round trip (struct field order, int vs
// float64, nested maps) must still verify after reopening the log.
func TestAudit_CanonicalDetailSurvivesReopen(t *testing.T) {
	l, path := newTestLog(t)
	ctx := context.Background()
	details := []map[string]any{
		{"s": detailStruct{Zeta: "z", Alpha: 1}},
		{"n": 42, "big": int64(1) << 52, "f": 1.5, "neg": -7},
		{"nested": map[string]any{"b": []any{1, "two", nil}, "a": true}},
		{"unicode": "naïve ✓ 日本", "html": "<script>&"},
		{},
		nil,
	}
	for i, d := range details {
		if err := l.Append(ctx, "t", "a", "r", d); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	reopened, err := NewLog(path, []byte("test-hmac-key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Verify(); err != nil {
		t.Fatalf("canonical detail mismatch after reopen: %v", err)
	}
}

func TestAudit_RekeyKeepsVersionsAndDetailProtection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	oldKey, newKey := []byte("old-key"), []byte("new-key")
	writeV1Log(t, path, oldKey, 2)
	l, err := NewLog(path, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Append(context.Background(), "agent", "tool.execute", "x", map[string]any{"ok": true})
	if err := l.Rekey(newKey); err != nil {
		t.Fatalf("rekey: %v", err)
	}
	if err := l.Verify(); err != nil {
		t.Fatalf("rekeyed log fails: %v", err)
	}
	lines := readLines(t, path)
	if strings.Contains(lines[0], `"v"`) || !strings.Contains(lines[2], `"v":2`) {
		t.Fatalf("rekey changed entry versions:\n%s", strings.Join(lines, "\n"))
	}
	rewriteEntry(t, path, 3, func(e *Entry) { e.Detail["ok"] = false })
	if err := l.Verify(); err == nil {
		t.Fatal("detail edit after rekey not detected")
	}
}
