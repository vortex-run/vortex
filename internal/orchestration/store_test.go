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
}
