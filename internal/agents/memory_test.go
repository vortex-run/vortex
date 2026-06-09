package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMemory_AppendSaveLoad(t *testing.T) {
	store := t.TempDir()
	m := NewMemory(store)
	m.SessionID = "sess-1"
	m.Append("user", "hello")
	m.Append("agent", "hi there")
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	// File exists at the expected path.
	if _, err := readFileExists(filepath.Join(store, "sess-1.json")); err != nil {
		t.Fatalf("memory file not written: %v", err)
	}
	// Load into a fresh memory.
	m2 := NewMemory(store)
	if err := m2.Load("sess-1"); err != nil {
		t.Fatal(err)
	}
	if len(m2.Messages) != 2 || m2.Messages[0].Content != "hello" || m2.Messages[1].Role != "agent" {
		t.Errorf("loaded messages = %+v", m2.Messages)
	}
}

func TestMemory_Recent(t *testing.T) {
	m := NewMemory(t.TempDir())
	for _, c := range []string{"a", "b", "c", "d"} {
		m.Append("user", c)
	}
	r := m.Recent(2)
	if len(r) != 2 || r[0].Content != "c" || r[1].Content != "d" {
		t.Errorf("Recent(2) = %+v, want [c d]", r)
	}
	if all := m.Recent(0); len(all) != 4 {
		t.Errorf("Recent(0) should return all 4, got %d", len(all))
	}
}

func TestMemory_Summary(t *testing.T) {
	m := NewMemory(t.TempDir())
	m.Append("system", "ready")
	m.Append("user", "build me a flutter app")
	if got := m.Summary(); got != "build me a flutter app" {
		t.Errorf("Summary = %q, want the first user message", got)
	}
}

func TestMemory_List(t *testing.T) {
	store := t.TempDir()
	for _, id := range []string{"s1", "s2"} {
		m := NewMemory(store)
		m.SessionID = id
		m.Append("user", "chat "+id)
		_ = m.Save()
	}
	list := NewMemory(store).List()
	if len(list) != 2 {
		t.Fatalf("List = %d sessions, want 2", len(list))
	}
	// Each has a summary.
	for _, s := range list {
		if s.Summary == "" || s.SessionID == "" {
			t.Errorf("incomplete session info: %+v", s)
		}
	}
}

func TestMemory_LoadMissingErrors(t *testing.T) {
	m := NewMemory(t.TempDir())
	if err := m.Load("nonexistent"); err == nil {
		t.Error("loading a missing session should error")
	}
}

func TestCoordinator_MemoryRecordsExchange(t *testing.T) {
	store := t.TempDir()
	c, _ := NewCoordinator(CoordinatorConfig{
		Bus:         NewBus(),
		AIGateway:   StubAIGateway{IntentReply: string(IntentGeneralQuestion), AnswerReply: "42"},
		MemoryStore: store,
	})
	if _, err := c.HandleMessage(testCtx(), "what is the answer?", "sx"); err != nil {
		t.Fatal(err)
	}
	hist := c.SessionHistory("sx")
	if len(hist) != 2 || hist[0].Role != "user" || hist[1].Content != "42" {
		t.Errorf("memory should record the exchange, got %+v", hist)
	}
	// Listed as a session.
	if len(c.ListSessions()) != 1 {
		t.Errorf("ListSessions should have 1 session")
	}
}

func readFileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	return err == nil, err
}

func testCtx() context.Context { return context.Background() }
