package orchestration

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newTestStore opens a RunStore on a temp-dir database.
func newTestStore(t *testing.T) *RunStore {
	t.Helper()
	s, err := NewRunStore(filepath.Join(t.TempDir(), "orchestration.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// chainTasks returns a 3-task dependency chain a → b → c with metadata.
func chainTasks() []*Task {
	return []*Task{
		{ID: "a", Name: "Task A", AgentType: "research", Input: "find things"},
		{ID: "b", Name: "Task B", AgentType: "devops", Input: "use them", DependsOn: []string{"a"},
			Meta: map[string]any{"k": "v"}},
		{ID: "c", Name: "Task C", AgentType: "general", Input: "wrap up", DependsOn: []string{"b"}},
	}
}

func TestRunStore_CreateAndLoadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateRun("build the thing", "sess-1", chainTasks())
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("empty run id")
	}

	tasks, memory, err := s.LoadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 || len(memory) != 0 {
		t.Fatalf("loaded %d tasks / %d memory, want 3 / 0", len(tasks), len(memory))
	}
	// Plan order must be preserved (Claim fairness depends on it).
	for i, want := range []string{"a", "b", "c"} {
		if tasks[i].ID != want {
			t.Errorf("tasks[%d].ID = %q, want %q (order not preserved)", i, tasks[i].ID, want)
		}
	}
	if tasks[1].DependsOn[0] != "a" || tasks[1].Meta["k"] != "v" {
		t.Errorf("JSON fields did not round-trip: %+v", tasks[1])
	}
	if tasks[0].State != StatePending {
		t.Errorf("state = %q, want pending (CreateRun stores the given state)", tasks[0].State)
	}
}

func TestRunStore_SaveTaskUpsertAndAttempts(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateRun("g", "", chainTasks())

	// Two running transitions bump attempts twice; terminal state persists.
	run := &Task{ID: "a", State: StateRunning, StartedAt: time.Now()}
	if err := s.SaveTask(id, run); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTask(id, run); err != nil {
		t.Fatal(err)
	}
	done := &Task{ID: "a", State: StateComplete, Result: "ra", FinishedAt: time.Now()}
	if err := s.SaveTask(id, done); err != nil {
		t.Fatal(err)
	}

	var attempts int
	var state, result string
	if err := s.db.QueryRow(
		`SELECT attempts, state, result FROM orch_tasks WHERE run_id=? AND id='a'`, id).
		Scan(&attempts, &state, &result); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || state != string(StateComplete) || result != "ra" {
		t.Errorf("attempts=%d state=%s result=%s, want 2/complete/ra", attempts, state, result)
	}
}

func TestRunStore_SyncTasksReconcilesAll(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateRun("g", "", chainTasks())

	final := chainTasks()
	final[0].State = StateComplete
	final[0].Result = "ra"
	final[1].State = StateFailed
	final[1].Error = "boom"
	final[2].State = StateFailed
	final[2].Error = "dependency b failed"
	if err := s.SyncTasks(id, final); err != nil {
		t.Fatal(err)
	}

	tasks, _, err := s.LoadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].State != StateComplete || tasks[1].State != StateFailed || tasks[2].Error == "" {
		t.Errorf("reconcile did not persist all states: %+v %+v %+v", tasks[0], tasks[1], tasks[2])
	}
}

func TestRunStore_SyncMemoryUpsertAndRoundTrip(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateRun("g", "", chainTasks())

	now := time.Now()
	snap := map[string]MemoryEntry{
		"task:a": {Key: "task:a", Value: "ra", Author: "a", UpdatedAt: now},
		"count":  {Key: "count", Value: float64(3), Author: "b", UpdatedAt: now},
	}
	if err := s.SyncMemory(id, snap); err != nil {
		t.Fatal(err)
	}
	// Upsert: overwrite one key.
	snap["task:a"] = MemoryEntry{Key: "task:a", Value: "ra2", Author: "a", UpdatedAt: now}
	if err := s.SyncMemory(id, snap); err != nil {
		t.Fatal(err)
	}

	_, memory, err := s.LoadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, e := range memory {
		got[e.Key] = e.Value
	}
	if got["task:a"] != "ra2" || got["count"] != float64(3) {
		t.Errorf("memory round-trip = %v", got)
	}
}

func TestRunStore_ListIncompleteMarkResumedFinish(t *testing.T) {
	s := newTestStore(t)
	id1, _ := s.CreateRun("first", "", chainTasks())
	id2, _ := s.CreateRun("second", "", chainTasks())

	if err := s.FinishRun(id2, RunDone, "summary"); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListIncomplete()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != id1 {
		t.Fatalf("incomplete = %+v, want only %s", runs, id1)
	}
	if runs[0].Status != RunRunning {
		t.Errorf("incomplete status = %q, want %q", runs[0].Status, RunRunning)
	}

	for want := 1; want <= 2; want++ {
		n, err := s.MarkResumed(id1)
		if err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("MarkResumed = %d, want %d", n, want)
		}
	}

	if err := s.FinishRun(id1, RunFailed, "exceeded resume attempts"); err != nil {
		t.Fatal(err)
	}
	runs, _ = s.ListIncomplete()
	if len(runs) != 0 {
		t.Errorf("after finish, incomplete = %+v, want none", runs)
	}
}

func TestRunStore_MetaCapabilityRoundTrip(t *testing.T) {
	s := newTestStore(t)
	caps := []string{"research.web", "code.read"}
	tasks := []*Task{{
		ID:    "t1",
		Name:  "cap task",
		Input: "do work",
		Meta: map[string]any{
			metaRequiredCapabilities: caps,
			metaPermissionProfile:    "read-mostly",
		},
	}}
	id, err := s.CreateRun("cap goal", "sess-cap", tasks)
	if err != nil {
		t.Fatal(err)
	}

	loaded, _, err := s.LoadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	assertMetaCaps(t, loaded[0].Meta, caps, "read-mostly")

	// SaveTask must preserve capability meta through another upsert.
	tasks[0].State = StateRunning
	tasks[0].StartedAt = time.Now()
	if err := s.SaveTask(id, tasks[0]); err != nil {
		t.Fatal(err)
	}
	loaded, _, err = s.LoadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	assertMetaCaps(t, loaded[0].Meta, caps, "read-mostly")

	// SyncTasks + FinishRun + reopen store (full persistence lifecycle).
	loaded[0].State = StateComplete
	loaded[0].Result = "done"
	loaded[0].FinishedAt = time.Now()
	if err := s.SyncTasks(id, loaded); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(id, RunDone, "summary"); err != nil {
		t.Fatal(err)
	}
	dbPath := s.path
	_ = s.Close()
	reopened, err := NewRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	loaded, _, err = reopened.LoadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	assertMetaCaps(t, loaded[0].Meta, caps, "read-mostly")
	recent, err := reopened.ListRecent(5)
	if err != nil || len(recent) != 1 || recent[0].Status != RunDone {
		t.Fatalf("ListRecent after reopen = %+v, err=%v", recent, err)
	}
}

func assertMetaCaps(t *testing.T, meta map[string]any, wantCaps []string, wantProfile string) {
	t.Helper()
	gotCaps, ok := meta[metaRequiredCapabilities].([]string)
	if !ok {
		t.Fatalf("required_capabilities type = %T, want []string; meta=%+v", meta[metaRequiredCapabilities], meta)
	}
	if len(gotCaps) != len(wantCaps) {
		t.Fatalf("caps = %v, want %v", gotCaps, wantCaps)
	}
	for i := range wantCaps {
		if gotCaps[i] != wantCaps[i] {
			t.Errorf("caps[%d] = %q, want %q", i, gotCaps[i], wantCaps[i])
		}
	}
	if meta[metaPermissionProfile] != wantProfile {
		t.Errorf("permission_profile = %v, want %q", meta[metaPermissionProfile], wantProfile)
	}
}

func TestRunStore_ListRecent(t *testing.T) {
	s := newTestStore(t)
	idRunning, _ := s.CreateRun("still going", "", chainTasks())
	idFailed, _ := s.CreateRun("will fail", "", chainTasks())
	idDone, _ := s.CreateRun("will finish", "", chainTasks())

	if err := s.FinishRun(idFailed, RunFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := s.FinishRun(idDone, RunDone, "ok"); err != nil {
		t.Fatal(err)
	}

	recent, err := s.ListRecent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 3 {
		t.Fatalf("ListRecent = %d runs, want 3", len(recent))
	}
	byID := map[string]string{}
	for _, r := range recent {
		byID[r.ID] = r.Status
	}
	if byID[idRunning] != RunRunning {
		t.Errorf("running run status = %q, want %q", byID[idRunning], RunRunning)
	}
	if byID[idFailed] != RunFailed {
		t.Errorf("failed run status = %q, want %q", byID[idFailed], RunFailed)
	}
	if byID[idDone] != RunDone {
		t.Errorf("done run status = %q, want %q", byID[idDone], RunDone)
	}
	// Newest updated_at first: done run finished after failed.
	if recent[0].ID != idDone {
		t.Errorf("ListRecent[0].ID = %s, want newest %s", recent[0].ID, idDone)
	}

	limited, err := s.ListRecent(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("ListRecent(2) = %d, want 2", len(limited))
	}
}

func TestRunStore_ListRecentTiebreak(t *testing.T) {
	s := newTestStore(t)
	var ids []string
	for i := 0; i < 5; i++ {
		id, err := s.CreateRun("g", "", chainTasks())
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// Force a millisecond tie across every run.
	if _, err := s.db.Exec(`UPDATE orch_runs SET created_at=1, updated_at=1`); err != nil {
		t.Fatal(err)
	}

	recent, err := s.ListRecent(0)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range recent {
		if want := ids[len(ids)-1-i]; r.ID != want {
			t.Fatalf("ListRecent[%d] = %s, want %s (newest insert first on tie)", i, r.ID, want)
		}
	}

	incomplete, err := s.ListIncomplete()
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range incomplete {
		if r.ID != ids[i] {
			t.Fatalf("ListIncomplete[%d] = %s, want %s (oldest insert first on tie)", i, r.ID, ids[i])
		}
	}
}

func TestRunStore_SaveTaskPreservesMetaWhenOmitted(t *testing.T) {
	s := newTestStore(t)
	caps := []string{"code.read"}
	id, err := s.CreateRun("g", "", []*Task{{
		ID: "t1", Name: "cap", Input: "x",
		Meta: map[string]any{
			metaRequiredCapabilities: caps,
			metaPermissionProfile:    "read-mostly",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTask(id, &Task{ID: "t1", State: StateRunning, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := s.LoadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	assertMetaCaps(t, loaded[0].Meta, caps, "read-mostly")
}

func TestRunStore_LoadRunNormalizesLegacyCapabilityJSON(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateRun("g", "", []*Task{{ID: "t1", Name: "x", Input: "y"}})
	if err != nil {
		t.Fatal(err)
	}
	legacy := `{"required_capabilities":["research.web"],"permission_profile":"p"}`
	if _, err := s.db.Exec(
		`UPDATE orch_tasks SET meta=? WHERE run_id=? AND id='t1'`, legacy, id); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := s.LoadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	assertMetaCaps(t, loaded[0].Meta, []string{"research.web"}, "p")
}

func TestRunStore_VerificationStatusMetaRoundTrip(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateRun("g", "", []*Task{{
		ID: "t1", Name: "v", Input: "x",
		Meta: map[string]any{"verification_status": "passed"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTask(id, &Task{
		ID: "t1", State: StateComplete, Result: "ok",
		Meta: map[string]any{"verification_status": "passed"},
	}); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := s.LoadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].Meta["verification_status"] != "passed" {
		t.Fatalf("verification_status = %v", loaded[0].Meta["verification_status"])
	}
}

// SyncTasks is the end-of-run reconcile that "self-corrects dropped
// best-effort writes" — that must include the attempt counter, otherwise a
// dropped SaveTask leaves attempts=0 for a task that ran twice.
func TestRunStore_SyncTasksPersistsAttempts(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateRun("g", "", chainTasks())
	if err != nil {
		t.Fatal(err)
	}
	final := chainTasks()
	final[0].State = StateComplete
	final[0].Result = "ra"
	final[0].Attempts = 2
	final[0].MaxAttempts = 2
	if err := s.SyncTasks(id, final); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := s.LoadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].Attempts != 2 || loaded[0].MaxAttempts != 2 {
		t.Fatalf("after SyncTasks attempts=%d max=%d, want 2/2", loaded[0].Attempts, loaded[0].MaxAttempts)
	}
	// A zero Attempts on a later reconcile must not regress the stored count
	// (legacy callers that only flip State never populate Attempts).
	final[0].Attempts = 0
	if err := s.SyncTasks(id, final); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ = s.LoadRun(id)
	if loaded[0].Attempts != 2 {
		t.Fatalf("zero Attempts regressed stored count to %d", loaded[0].Attempts)
	}
}

// SaveTask with t.Attempts set is authoritative and must not be double-counted
// by the legacy SQL bump, across the Claim -> SaveTask(running) ->
// SaveTask(terminal) sequence the orchestrator uses.
func TestRunStore_SaveTaskAuthoritativeAttemptsNoDoubleCount(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateRun("g", "", chainTasks())
	q := NewTaskQueue()
	if err := q.Add(&Task{ID: "a", MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	claimed := q.Claim()
	if err := s.SaveTask(id, claimed); err != nil {
		t.Fatal(err)
	}
	if _, err := q.FailOrRetry("a", "flaky"); err != nil {
		t.Fatal(err)
	}
	pending, _ := q.Get("a")
	if err := s.SaveTask(id, pending); err != nil {
		t.Fatal(err)
	}
	claimed = q.Claim()
	if err := s.SaveTask(id, claimed); err != nil {
		t.Fatal(err)
	}
	_ = q.Complete("a", "ok")
	final, _ := q.Get("a")
	if err := s.SaveTask(id, final); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := s.LoadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].Attempts != 2 || loaded[0].State != StateComplete {
		t.Fatalf("attempts=%d state=%s, want 2/complete", loaded[0].Attempts, loaded[0].State)
	}
}

// A hand-edited or corrupted meta column must not crash SaveTask: a JSON
// `null` unmarshals to a nil map, and unparseable JSON is dropped and
// rewritten from the in-memory task (see taskMetaForUpsert).
func TestRunStore_SaveTaskToleratesCorruptStoredMeta(t *testing.T) {
	for _, bad := range []string{"null", "not json", "[1,2]"} {
		t.Run(bad, func(t *testing.T) {
			s := newTestStore(t)
			id, err := s.CreateRun("g", "", []*Task{{ID: "t1", Name: "x", Input: "y"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(
				`UPDATE orch_tasks SET meta=? WHERE run_id=? AND id='t1'`, bad, id); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveTask(id, &Task{ID: "t1", State: StateRunning, MaxAttempts: 2, Attempts: 1}); err != nil {
				t.Fatal(err)
			}
			loaded, _, err := s.LoadRun(id)
			if err != nil {
				t.Fatal(err)
			}
			if loaded[0].MaxAttempts != 2 || loaded[0].Meta != nil {
				t.Fatalf("after corrupt meta: %+v", loaded[0])
			}
		})
	}
}

// Contract: a non-empty Meta on SaveTask/SyncTasks replaces the stored meta
// wholesale (no merge); an empty/nil Meta preserves it. Callers therefore
// cannot clear meta to {} through SaveTask — the queue only ever adds keys
// (SetMeta merges), so the orchestrator never needs to.
func TestRunStore_SaveTaskNonEmptyMetaReplaces(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateRun("g", "", []*Task{{
		ID: "t1", Name: "x", Input: "y",
		Meta: map[string]any{metaRequiredCapabilities: []string{"code.read"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTask(id, &Task{ID: "t1", State: StateRunning, Meta: map[string]any{"only": "this"}}); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := s.LoadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := loaded[0].Meta[metaRequiredCapabilities]; still || loaded[0].Meta["only"] != "this" {
		t.Fatalf("non-empty Meta should replace stored meta, got %+v", loaded[0].Meta)
	}
	// Empty (non-nil) map behaves like nil: preserve.
	if err := s.SaveTask(id, &Task{ID: "t1", State: StateComplete, Meta: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ = s.LoadRun(id)
	if loaded[0].Meta["only"] != "this" {
		t.Fatalf("empty Meta should preserve stored meta, got %+v", loaded[0].Meta)
	}
}

func TestRunStore_ConcurrentSaveTask(t *testing.T) {
	s := newTestStore(t)
	tasks := chainTasks()
	id, _ := s.CreateRun("g", "", tasks)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		for _, tk := range tasks {
			wg.Add(1)
			go func(taskID string) {
				defer wg.Done()
				if err := s.SaveTask(id, &Task{ID: taskID, State: StateRunning}); err != nil {
					t.Errorf("concurrent SaveTask: %v", err)
				}
			}(tk.ID)
		}
	}
	wg.Wait()

	loaded, _, err := s.LoadRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 3 {
		t.Errorf("tasks after concurrent writes = %d, want 3", len(loaded))
	}
	for _, tk := range loaded {
		if tk.ID == "b" && (tk.Meta == nil || tk.Meta["k"] != "v") {
			t.Errorf("task b meta after concurrent SaveTask = %+v, want k=v preserved", tk.Meta)
		}
	}
}
