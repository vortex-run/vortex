package agents

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestEpisodicStore(t *testing.T) *EpisodicStore {
	t.Helper()
	store, err := NewEpisodicStore(filepath.Join(t.TempDir(), "episodes.db"))
	if err != nil {
		t.Fatalf("NewEpisodicStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestEpisodicStoreStoreAndRecall(t *testing.T) {
	store := newTestEpisodicStore(t)
	ep := Episode{
		Content:    "User prefers Python over JavaScript",
		Context:    "preferences",
		Importance: 0.9,
		Tags:       []string{"python", "preference"},
		SessionID:  "s1",
	}
	if err := store.Store(ep); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, err := store.Recall("which language does the user like, python?", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Recall returned %d episodes, want 1", len(got))
	}
	if got[0].Content != ep.Content || got[0].Context != ep.Context {
		t.Errorf("recalled episode = %+v", got[0])
	}
	if len(got[0].Tags) != 2 {
		t.Errorf("tags = %v, want 2 entries", got[0].Tags)
	}
	if got[0].ID == "" || got[0].Timestamp.IsZero() {
		t.Error("ID/timestamp not assigned on store")
	}
}

func TestEpisodicStoreRecallUnrelatedEmpty(t *testing.T) {
	store := newTestEpisodicStore(t)
	if err := store.Store(Episode{Content: "deployed to production"}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := store.Recall("favourite pizza topping", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Recall returned %d episodes for unrelated query, want 0", len(got))
	}
}

func TestEpisodicStoreValidationAndClamping(t *testing.T) {
	store := newTestEpisodicStore(t)
	if err := store.Store(Episode{Content: "  "}); err == nil {
		t.Error("Store with blank content should error")
	}
	if err := store.Store(Episode{Content: "over-important fact", Importance: 7}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, _ := store.Recall("over-important fact", 1)
	if len(got) != 1 || got[0].Importance != 1.0 {
		t.Errorf("importance not clamped to 1.0: %+v", got)
	}
}

func TestEpisodicStoreRecencyWeighting(t *testing.T) {
	store := newTestEpisodicStore(t)
	old := Episode{
		Content: "deployment checklist reviewed", Importance: 0.5,
		Timestamp: time.Now().AddDate(0, 0, -30),
	}
	recent := Episode{
		Content: "deployment completed successfully", Importance: 0.5,
		Timestamp: time.Now(),
	}
	for _, ep := range []Episode{old, recent} {
		if err := store.Store(ep); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}
	got, err := store.Recall("deployment", 5)
	if err != nil || len(got) != 2 {
		t.Fatalf("Recall = %d episodes (%v), want 2", len(got), err)
	}
	if got[0].Content != recent.Content {
		t.Errorf("first recall = %q, want the recent episode", got[0].Content)
	}

	// Importance still dominates recency: a vital old fact beats both.
	vital := Episode{
		Content: "deployment requires VPN access", Importance: 1.0,
		Timestamp: time.Now().AddDate(0, 0, -5),
	}
	if err := store.Store(vital); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, _ = store.Recall("deployment", 5)
	if len(got) != 3 || got[0].Content != vital.Content {
		t.Errorf("first recall = %q, want the vital episode", got[0].Content)
	}
}

func TestStoreImportantFiltersTrivial(t *testing.T) {
	store := newTestEpisodicStore(t)
	gw := &recordingGateway{reply: "[]"}
	if err := store.StoreImportant(context.Background(), gw, "s1", "user: hi\nagent: hello!"); err != nil {
		t.Fatalf("StoreImportant: %v", err)
	}
	if !gw.called {
		t.Fatal("gateway not consulted")
	}
	if got, _ := store.Recall("hi hello", 5); len(got) != 0 {
		t.Errorf("trivial exchange stored %d episodes, want 0", len(got))
	}
}

func TestStoreImportantStoresFacts(t *testing.T) {
	store := newTestEpisodicStore(t)
	gw := &recordingGateway{reply: "```json\n" + `[
	  {"content": "Project X uses FastAPI and PostgreSQL", "context": "project-x", "importance": 0.9, "tags": ["stack"]},
	  {"content": "", "context": "ignored", "importance": 0.5}
	]` + "\n```"}
	if err := store.StoreImportant(context.Background(), gw, "s1", "user: we use fastapi\nagent: noted"); err != nil {
		t.Fatalf("StoreImportant: %v", err)
	}
	got, _ := store.Recall("what stack does project x use? fastapi", 5)
	if len(got) != 1 {
		t.Fatalf("Recall = %d episodes, want 1 (blank content skipped)", len(got))
	}
	if got[0].Importance != 0.9 || got[0].SessionID != "s1" {
		t.Errorf("stored fact = %+v", got[0])
	}
}

func TestStoreImportantBadJSONErrors(t *testing.T) {
	store := newTestEpisodicStore(t)
	gw := &recordingGateway{reply: "no facts here, sorry"}
	if err := store.StoreImportant(context.Background(), gw, "s1", "user: x\nagent: y"); err == nil {
		t.Error("expected parse error for non-JSON reply")
	}
}

func TestProjectMemoryScoped(t *testing.T) {
	store := newTestEpisodicStore(t)
	projA := store.ForProject("/work/project-a")
	projB := store.ForProject("/work/project-b")

	if err := projA.Store(Episode{Content: "uses FastAPI backend", Importance: 0.8}); err != nil {
		t.Fatalf("Store A: %v", err)
	}
	if err := projB.Store(Episode{Content: "uses FastAPI with Celery", Importance: 0.8}); err != nil {
		t.Fatalf("Store B: %v", err)
	}

	got, err := projA.Recall("fastapi", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 1 || got[0].Context != "/work/project-a" {
		t.Fatalf("project A recall = %+v, want only project A's episode", got)
	}
	// Unscoped recall sees both.
	if all, _ := store.Recall("fastapi", 5); len(all) != 2 {
		t.Errorf("global recall = %d episodes, want 2", len(all))
	}
}

func TestCoordinatorRecallsMemoriesIntoPrompt(t *testing.T) {
	gw := &skillCapturingGateway{}
	c := newTestCoordinator(t, gw)
	store := newTestEpisodicStore(t)
	c.SetEpisodicStore(store)
	if err := store.Store(Episode{
		Content: "User prefers Python over JavaScript", Importance: 0.9,
		Tags: []string{"python"},
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if _, err := c.HandleMessage(context.Background(), "should I write this in python?", "s1"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	var found bool
	for _, p := range gw.prompts() {
		if strings.Contains(p, "Relevant memories:") && strings.Contains(p, "prefers Python") {
			found = true
		}
	}
	if !found {
		t.Error("recalled memory not included in system prompt")
	}
}
