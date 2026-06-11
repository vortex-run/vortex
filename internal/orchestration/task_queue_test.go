package orchestration

import (
	"strings"
	"sync"
	"testing"
)

func TestQueue_AddAndGet(t *testing.T) {
	q := NewTaskQueue()
	if err := q.Add(&Task{ID: "a", Name: "first"}); err != nil {
		t.Fatal(err)
	}
	got, ok := q.Get("a")
	if !ok || got.Name != "first" || got.State != StatePending {
		t.Errorf("Get = %+v, ok=%v", got, ok)
	}
}

func TestQueue_AddRejectsDuplicateAndEmpty(t *testing.T) {
	q := NewTaskQueue()
	_ = q.Add(&Task{ID: "a"})
	if err := q.Add(&Task{ID: "a"}); err == nil {
		t.Error("duplicate ID should error")
	}
	if err := q.Add(&Task{ID: ""}); err == nil {
		t.Error("empty ID should error")
	}
}

func TestQueue_ClaimNoDeps(t *testing.T) {
	q := NewTaskQueue()
	_ = q.Add(&Task{ID: "a"})
	_ = q.Add(&Task{ID: "b"})
	first := q.Claim()
	if first == nil || first.ID != "a" || first.State != StateRunning {
		t.Errorf("first claim = %+v", first)
	}
	second := q.Claim()
	if second == nil || second.ID != "b" {
		t.Errorf("second claim = %+v", second)
	}
	// No more ready tasks (both running).
	if q.Claim() != nil {
		t.Error("no tasks should be claimable while others run")
	}
}

func TestQueue_ClaimRespectsDependencies(t *testing.T) {
	q := NewTaskQueue()
	_ = q.Add(&Task{ID: "a"})
	_ = q.Add(&Task{ID: "b", DependsOn: []string{"a"}})

	// b is not ready until a completes.
	first := q.Claim()
	if first.ID != "a" {
		t.Fatalf("expected a first, got %s", first.ID)
	}
	if q.Claim() != nil {
		t.Error("b should not be claimable before a completes")
	}
	_ = q.Complete("a", "done")
	now := q.Claim()
	if now == nil || now.ID != "b" {
		t.Errorf("b should be claimable after a completes, got %+v", now)
	}
}

func TestQueue_FailedDependencyFailsDependent(t *testing.T) {
	q := NewTaskQueue()
	_ = q.Add(&Task{ID: "a"})
	_ = q.Add(&Task{ID: "b", DependsOn: []string{"a"}})

	_ = q.Claim() // claim a
	_ = q.Fail("a", "boom")
	// b should not be claimable; claiming should mark it failed.
	if q.Claim() != nil {
		t.Error("b should not run when its dependency failed")
	}
	b, _ := q.Get("b")
	if b.State != StateFailed || b.Error != "dependency failed" {
		t.Errorf("b = %+v, want failed due to dependency", b)
	}
}

func TestQueue_DiamondDependencies(t *testing.T) {
	// a → {b, c} → d
	q := NewTaskQueue()
	_ = q.Add(&Task{ID: "a"})
	_ = q.Add(&Task{ID: "b", DependsOn: []string{"a"}})
	_ = q.Add(&Task{ID: "c", DependsOn: []string{"a"}})
	_ = q.Add(&Task{ID: "d", DependsOn: []string{"b", "c"}})

	_ = q.Claim()           // a
	_ = q.Complete("a", "") // unlocks b, c
	b := q.Claim()
	c := q.Claim()
	if b == nil || c == nil {
		t.Fatal("b and c should both be claimable after a")
	}
	if q.Claim() != nil {
		t.Error("d should wait for both b and c")
	}
	_ = q.Complete(b.ID, "")
	if q.Claim() != nil {
		t.Error("d should wait for the second of b/c")
	}
	_ = q.Complete(c.ID, "")
	d := q.Claim()
	if d == nil || d.ID != "d" {
		t.Errorf("d should be claimable after b and c, got %+v", d)
	}
}

func TestQueue_DoneAndStats(t *testing.T) {
	q := NewTaskQueue()
	_ = q.Add(&Task{ID: "a"})
	_ = q.Add(&Task{ID: "b"})
	if q.Done() {
		t.Error("not done with pending tasks")
	}
	_ = q.Claim()
	_ = q.Complete("a", "")
	_ = q.Claim()
	_ = q.Fail("b", "x")
	if !q.Done() {
		t.Error("should be done when all terminal")
	}
	stats := q.Stats()
	if stats[StateComplete] != 1 || stats[StateFailed] != 1 {
		t.Errorf("stats = %v", stats)
	}
}

func TestQueue_HasCycle(t *testing.T) {
	q := NewTaskQueue()
	_ = q.Add(&Task{ID: "a", DependsOn: []string{"b"}})
	_ = q.Add(&Task{ID: "b", DependsOn: []string{"a"}})
	if !q.HasCycle() {
		t.Error("a↔b should be detected as a cycle")
	}

	q2 := NewTaskQueue()
	_ = q2.Add(&Task{ID: "a"})
	_ = q2.Add(&Task{ID: "b", DependsOn: []string{"a"}})
	_ = q2.Add(&Task{ID: "c", DependsOn: []string{"b"}})
	if q2.HasCycle() {
		t.Error("a→b→c is acyclic")
	}
}

func TestQueue_CompleteUnknownErrors(t *testing.T) {
	q := NewTaskQueue()
	if err := q.Complete("nope", ""); err == nil {
		t.Error("completing an unknown task should error")
	}
}

func TestQueue_ConcurrentClaim(t *testing.T) {
	q := NewTaskQueue()
	for i := 0; i < 50; i++ {
		_ = q.Add(&Task{ID: string(rune('A'+i%26)) + string(rune('0'+i/26))})
	}
	var wg sync.WaitGroup
	claimed := make(chan string, 50)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				t := q.Claim()
				if t == nil {
					return
				}
				claimed <- t.ID
				_ = q.Complete(t.ID, "")
			}
		}()
	}
	wg.Wait()
	close(claimed)
	// Each task claimed exactly once (no double-claim under concurrency).
	seen := map[string]bool{}
	for id := range claimed {
		if seen[id] {
			t.Errorf("task %s claimed twice", id)
		}
		seen[id] = true
	}
	if len(seen) != 50 {
		t.Errorf("claimed %d unique tasks, want 50", len(seen))
	}
}

func TestValidate_RejectsUnknownDependency(t *testing.T) {
	q := NewTaskQueue()
	if err := q.Add(&Task{ID: "a", DependsOn: []string{"ghost"}}); err != nil {
		t.Fatal(err)
	}
	err := q.Validate()
	if err == nil {
		t.Fatal("Validate should reject an unknown dependency")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the missing dep: %v", err)
	}
}

func TestValidate_AcceptsResolvableGraph(t *testing.T) {
	q := NewTaskQueue()
	_ = q.Add(&Task{ID: "a"})
	_ = q.Add(&Task{ID: "b", DependsOn: []string{"a"}})
	_ = q.Add(&Task{ID: "c", DependsOn: []string{"a", "b"}})
	if err := q.Validate(); err != nil {
		t.Errorf("valid graph should pass Validate: %v", err)
	}
}

func TestValidate_RejectsCycle(t *testing.T) {
	q := NewTaskQueue()
	_ = q.Add(&Task{ID: "a", DependsOn: []string{"b"}})
	_ = q.Add(&Task{ID: "b", DependsOn: []string{"a"}})
	if err := q.Validate(); err == nil {
		t.Error("Validate should reject a cycle")
	}
}

func TestClaim_FailsTaskWithUnknownDependency(t *testing.T) {
	// Defense-in-depth: even if a task with an unknown dep slips past Validate
	// (e.g. added later), Claim must not strand it — it is marked failed.
	q := NewTaskQueue()
	_ = q.Add(&Task{ID: "a", DependsOn: []string{"ghost"}})
	if claimed := q.Claim(); claimed != nil {
		t.Errorf("task with unknown dep should not be claimable, got %+v", claimed)
	}
	got, _ := q.Get("a")
	if got.State != StateFailed {
		t.Errorf("task with unknown dep should be marked failed, got %s", got.State)
	}
}
