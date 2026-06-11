package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *MemoryStore {
	t.Helper()
	s, err := NewMemoryStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMemoryStore_CreatesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "memory.db")
	s, err := NewMemoryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("database file not created: %v", err)
	}
}

func TestMemoryStore_AppendAndRecent(t *testing.T) {
	s := newTestStore(t)
	sid, err := s.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		role, content string
	}{
		{"user", "first"},
		{"agent", "second"},
		{"user", "third"},
	} {
		if err := s.AppendMessage(sid, m.role, m.content, nil); err != nil {
			t.Fatal(err)
		}
	}

	all, err := s.Recent(sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("Recent(0) = %d messages, want 3", len(all))
	}
	if all[0].Content != "first" || all[2].Content != "third" {
		t.Errorf("messages out of chronological order: %+v", all)
	}

	last2, err := s.Recent(sid, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(last2) != 2 || last2[0].Content != "second" || last2[1].Content != "third" {
		t.Errorf("Recent(2) = %+v, want [second third]", last2)
	}
}

func TestMemoryStore_AppendStoresToolCalls(t *testing.T) {
	s := newTestStore(t)
	sid, _ := s.NewSession()
	if err := s.AppendMessage(sid, "agent", "ran tools", []string{"http_get", "write_file"}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := s.Recent(sid, 0)
	if len(msgs) != 1 || len(msgs[0].ToolCalls) != 2 || msgs[0].ToolCalls[0] != "http_get" {
		t.Errorf("tool calls not stored: %+v", msgs)
	}
}

func TestMemoryStore_ListSessions(t *testing.T) {
	s := newTestStore(t)
	a, _ := s.NewSession()
	if err := s.AppendMessage(a, "user", "session A question", nil); err != nil {
		t.Fatal(err)
	}
	b, _ := s.NewSession()
	if err := s.AppendMessage(b, "user", "session B question", nil); err != nil {
		t.Fatal(err)
	}

	sessions, err := s.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("ListSessions = %d, want 2", len(sessions))
	}
	// Newest-updated first: b was appended last.
	if sessions[0].SessionID != b {
		t.Errorf("ListSessions[0] = %s, want most recent %s", sessions[0].SessionID, b)
	}
	if sessions[0].Summary == "" {
		t.Error("session summary should be set from first user message")
	}
}

func TestMemoryStore_SearchMessages(t *testing.T) {
	s := newTestStore(t)
	sid, _ := s.NewSession()
	_ = s.AppendMessage(sid, "user", "how do I configure nginx reverse proxy", nil)
	_ = s.AppendMessage(sid, "agent", "edit the upstream block", nil)
	_ = s.AppendMessage(sid, "user", "what about TLS certificates", nil)

	hits, err := s.SearchMessages("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("SearchMessages(nginx) = %d hits, want 1", len(hits))
	}
	if hits[0].SessionID != sid {
		t.Errorf("hit session = %s, want %s", hits[0].SessionID, sid)
	}

	if hits, _ := s.SearchMessages("certificates"); len(hits) != 1 {
		t.Errorf("SearchMessages(certificates) = %d, want 1", len(hits))
	}
	if hits, _ := s.SearchMessages("nonexistentword"); len(hits) != 0 {
		t.Errorf("SearchMessages(nonexistentword) = %d, want 0", len(hits))
	}
	// Empty query returns nothing, not an error.
	if hits, err := s.SearchMessages("  "); err != nil || hits != nil {
		t.Errorf("empty query: hits=%v err=%v", hits, err)
	}
}

func TestMemoryStore_DeleteSession(t *testing.T) {
	s := newTestStore(t)
	sid, _ := s.NewSession()
	_ = s.AppendMessage(sid, "user", "to be deleted", nil)
	if err := s.DeleteSession(sid); err != nil {
		t.Fatal(err)
	}
	sessions, _ := s.ListSessions()
	if len(sessions) != 0 {
		t.Errorf("session not deleted: %d remain", len(sessions))
	}
	// Messages cascade-deleted: search finds nothing.
	if hits, _ := s.SearchMessages("deleted"); len(hits) != 0 {
		t.Errorf("messages not cascade-deleted: %d hits", len(hits))
	}
}

func TestMemoryStore_Stats(t *testing.T) {
	s := newTestStore(t)
	a, _ := s.NewSession()
	_ = s.AppendMessage(a, "user", "one", nil)
	_ = s.AppendMessage(a, "agent", "two", nil)
	b, _ := s.NewSession()
	_ = s.AppendMessage(b, "user", "three", nil)

	st := s.Stats()
	if st.TotalSessions != 2 {
		t.Errorf("TotalSessions = %d, want 2", st.TotalSessions)
	}
	if st.TotalMessages != 3 {
		t.Errorf("TotalMessages = %d, want 3", st.TotalMessages)
	}
	if st.DBSizeMB <= 0 {
		t.Errorf("DBSizeMB = %v, want > 0", st.DBSizeMB)
	}
}

func TestMemoryStore_MigrateJSONDir(t *testing.T) {
	// Write two legacy JSON session files.
	dir := t.TempDir()
	now := time.Now()
	for _, d := range []memoryData{
		{
			SessionID: "legacy-1", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-30 * time.Minute),
			Messages: []MemoryMessage{
				{Role: "user", Content: "legacy question one", Timestamp: now.Add(-time.Hour)},
				{Role: "agent", Content: "legacy answer one", Timestamp: now.Add(-59 * time.Minute)},
			},
		},
		{
			SessionID: "legacy-2", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-90 * time.Minute),
			Messages: []MemoryMessage{
				{Role: "user", Content: "legacy question two", Timestamp: now.Add(-2 * time.Hour)},
			},
		},
	} {
		b, _ := json.Marshal(d)
		if err := os.WriteFile(filepath.Join(dir, d.SessionID+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s := newTestStore(t)
	n, err := s.MigrateJSONDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("migrated %d sessions, want 2", n)
	}

	// Migrated data is queryable.
	sessions, _ := s.ListSessions()
	if len(sessions) != 2 {
		t.Errorf("after migration ListSessions = %d, want 2", len(sessions))
	}
	msgs, _ := s.Recent("legacy-1", 0)
	if len(msgs) != 2 || msgs[0].Content != "legacy question one" {
		t.Errorf("migrated messages wrong: %+v", msgs)
	}
	if hits, _ := s.SearchMessages("legacy"); len(hits) == 0 {
		t.Error("migrated messages should be searchable")
	}

	// Re-running is idempotent: nothing new imported.
	n2, err := s.MigrateJSONDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("re-migration imported %d, want 0 (idempotent)", n2)
	}
}

func TestMemoryStore_MigrateMissingDirIsNoOp(t *testing.T) {
	s := newTestStore(t)
	n, err := s.MigrateJSONDir(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Errorf("missing dir should not error: %v", err)
	}
	if n != 0 {
		t.Errorf("missing dir imported %d, want 0", n)
	}
}
