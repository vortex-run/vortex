package audit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestLog(t *testing.T) (*Log, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := NewLog(path, []byte("test-hmac-key"))
	if err != nil {
		t.Fatal(err)
	}
	return l, path
}

func TestAudit_AppendCreatesEntry(t *testing.T) {
	l, _ := newTestLog(t)
	if err := l.Append(context.Background(), "alice", "secret.set", "DB_PASSWORD", map[string]any{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	entries, err := l.Query(QueryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Actor != "alice" || e.Action != "secret.set" || e.Resource != "DB_PASSWORD" {
		t.Errorf("entry fields wrong: %+v", e)
	}
	if e.Seq != 1 || e.Hash == "" {
		t.Errorf("seq/hash wrong: seq=%d hash=%q", e.Seq, e.Hash)
	}
}

func TestAudit_AppendIncrementsSeq(t *testing.T) {
	l, _ := newTestLog(t)
	for i := 0; i < 3; i++ {
		if err := l.Append(context.Background(), "sys", "act", "res", nil); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := l.Query(QueryFilter{})
	for i, e := range entries {
		if e.Seq != uint64(i+1) {
			t.Errorf("entry %d seq = %d, want %d", i, e.Seq, i+1)
		}
	}
}

func TestAudit_VerifyUntampered(t *testing.T) {
	l, _ := newTestLog(t)
	for i := 0; i < 5; i++ {
		_ = l.Append(context.Background(), "sys", "act", "res", nil)
	}
	if err := l.Verify(); err != nil {
		t.Errorf("Verify on untampered log = %v, want nil", err)
	}
}

func TestAudit_VerifyDetectsModification(t *testing.T) {
	l, path := newTestLog(t)
	_ = l.Append(context.Background(), "a", "act1", "r", nil)
	_ = l.Append(context.Background(), "b", "act2", "r", nil)
	_ = l.Append(context.Background(), "c", "act3", "r", nil)

	// Tamper with the middle entry's actor, keeping its (now-stale) hash.
	lines := readLines(t, path)
	lines[1] = strings.Replace(lines[1], `"actor":"b"`, `"actor":"EVIL"`, 1)
	writeLines(t, path, lines)

	err := l.Verify()
	if err == nil {
		t.Fatal("Verify should detect the modified entry")
	}
	if !strings.Contains(err.Error(), "entry 2") {
		t.Errorf("error should name entry 2: %v", err)
	}
}

func TestAudit_VerifyDetectsDeletion(t *testing.T) {
	l, path := newTestLog(t)
	_ = l.Append(context.Background(), "a", "act1", "r", nil)
	_ = l.Append(context.Background(), "b", "act2", "r", nil)
	_ = l.Append(context.Background(), "c", "act3", "r", nil)

	// Delete the middle entry, leaving seq 1,3 — the chain must break.
	lines := readLines(t, path)
	writeLines(t, path, []string{lines[0], lines[2]})

	if err := l.Verify(); err == nil {
		t.Error("Verify should detect the deleted entry")
	}
}

func TestAudit_QueryByActor(t *testing.T) {
	l, _ := newTestLog(t)
	_ = l.Append(context.Background(), "alice", "act", "r", nil)
	_ = l.Append(context.Background(), "bob", "act", "r", nil)
	_ = l.Append(context.Background(), "alice", "act", "r", nil)

	got, err := l.Query(QueryFilter{Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("alice entries = %d, want 2", len(got))
	}
}

func TestAudit_QueryByAction(t *testing.T) {
	l, _ := newTestLog(t)
	_ = l.Append(context.Background(), "a", "secret.set", "r", nil)
	_ = l.Append(context.Background(), "a", "config.reload", "r", nil)
	_ = l.Append(context.Background(), "a", "secret.set", "r", nil)

	got, _ := l.Query(QueryFilter{Action: "secret.set"})
	if len(got) != 2 {
		t.Errorf("secret.set entries = %d, want 2", len(got))
	}
}

func TestAudit_QueryBySinceUntil(t *testing.T) {
	l, _ := newTestLog(t)
	_ = l.Append(context.Background(), "a", "act", "r", nil)
	time.Sleep(10 * time.Millisecond)
	mid := time.Now().UTC()
	time.Sleep(10 * time.Millisecond)
	_ = l.Append(context.Background(), "a", "act", "r", nil)

	since, err := l.Query(QueryFilter{Since: mid})
	if err != nil {
		t.Fatal(err)
	}
	if len(since) != 1 {
		t.Errorf("Since filter = %d entries, want 1", len(since))
	}
	until, _ := l.Query(QueryFilter{Until: mid})
	if len(until) != 1 {
		t.Errorf("Until filter = %d entries, want 1", len(until))
	}
}

func TestAudit_QueryLimitNewestFirst(t *testing.T) {
	l, _ := newTestLog(t)
	for i := 0; i < 5; i++ {
		_ = l.Append(context.Background(), "a", "act", "r", nil)
	}
	got, err := l.Query(QueryFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("limit result = %d, want 2", len(got))
	}
	// Newest first: seq 5 then 4.
	if got[0].Seq != 5 || got[1].Seq != 4 {
		t.Errorf("limit order = [%d %d], want [5 4]", got[0].Seq, got[1].Seq)
	}
}

func TestAudit_ConcurrentAppend(t *testing.T) {
	l, _ := newTestLog(t)
	var wg sync.WaitGroup
	const n = 50
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Append(context.Background(), "a", "act", "r", nil); err != nil {
				t.Errorf("concurrent Append: %v", err)
			}
		}()
	}
	wg.Wait()

	entries, _ := l.Query(QueryFilter{})
	if len(entries) != n {
		t.Errorf("entries = %d, want %d", len(entries), n)
	}
	// Every entry present and chain intact.
	if err := l.Verify(); err != nil {
		t.Errorf("Verify after concurrent appends: %v", err)
	}
	seen := make(map[uint64]bool)
	for _, e := range entries {
		if seen[e.Seq] {
			t.Errorf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
	}
}

// readLines reads a file into a slice of non-empty lines.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ln := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

// writeLines writes lines back to a file, one per line.
func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
