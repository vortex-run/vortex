package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueue_FailOrRetry_RequeuesUntilMax(t *testing.T) {
	q := NewTaskQueue()
	if err := q.Add(&Task{ID: "a", MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	claimed := q.Claim()
	if claimed == nil || claimed.Attempts != 1 {
		t.Fatalf("first claim = %+v", claimed)
	}
	retried, err := q.FailOrRetry("a", "transient")
	if err != nil || !retried {
		t.Fatalf("FailOrRetry: retried=%v err=%v", retried, err)
	}
	got, _ := q.Get("a")
	if got.State != StatePending || got.Attempts != 1 {
		t.Fatalf("after retry pending: %+v", got)
	}
	claimed = q.Claim()
	if claimed == nil || claimed.Attempts != 2 {
		t.Fatalf("second claim = %+v", claimed)
	}
	retried, err = q.FailOrRetry("a", "still bad")
	if err != nil || retried {
		t.Fatalf("second FailOrRetry should fail terminal: retried=%v err=%v", retried, err)
	}
	got, _ = q.Get("a")
	if got.State != StateFailed || !strings.Contains(got.Error, "still bad") {
		t.Fatalf("terminal fail: %+v", got)
	}
}

func TestQueue_Add_RejectsMaxAttemptsAboveCap(t *testing.T) {
	q := NewTaskQueue()
	if err := q.Add(&Task{ID: "a", MaxAttempts: maxTaskAttempts + 1}); err == nil {
		t.Fatal("expected max_attempts rejection")
	}
}

func TestValidatePlan_RejectsMaxAttemptsOutOfRange(t *testing.T) {
	err := validatePlan([]*Task{{ID: "t1", AgentType: "general", MaxAttempts: 99}})
	if err == nil || !strings.Contains(err.Error(), "max_attempts") {
		t.Fatalf("want max_attempts error, got %v", err)
	}
}

func TestRun_BoundedRetryThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	o, err := NewOrchestrator(OrchestratorConfig{
		Executor: ExecutorFunc(func(_ context.Context, _ *Task, _ *SharedMemory) (string, error) {
			n := calls.Add(1)
			if n == 1 {
				return "", errors.New("flaky")
			}
			return "ok", nil
		}),
		MaxParallel: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := o.Run(context.Background(), "goal", []*Task{
		{ID: "a", Name: "flaky", AgentType: "general", MaxAttempts: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || res.Completed != 1 || res.Failed != 0 {
		t.Fatalf("calls=%d completed=%d failed=%d", calls.Load(), res.Completed, res.Failed)
	}
}

// A verification failure on attempt 1 records verification_error; when the
// retry passes, the stale error must not survive next to status "passed".
func TestRun_VerificationRetryClearsStaleError(t *testing.T) {
	var calls atomic.Int32
	o, err := NewOrchestrator(OrchestratorConfig{
		Executor: ExecutorFunc(func(_ context.Context, _ *Task, _ *SharedMemory) (string, error) {
			if calls.Add(1) == 1 {
				return "missing token", nil
			}
			return "has TOKEN", nil
		}),
		MaxParallel: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := o.Run(context.Background(), "goal", []*Task{
		{ID: "a", AgentType: "general", MaxAttempts: 2, VerificationCriteria: "contains:TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := res.Tasks[0]
	if got.State != StateComplete || got.Meta["verification_status"] != "passed" {
		t.Fatalf("task = %+v", got)
	}
	if v, stale := got.Meta["verification_error"]; stale {
		t.Fatalf("verification_error %q survived a passing retry", v)
	}
}

func TestQueue_SetMetaNilValueDeletesKey(t *testing.T) {
	q := NewTaskQueue()
	if err := q.Add(&Task{ID: "a", Meta: map[string]any{"k": "v", "gone": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetMeta("a", map[string]any{"gone": nil, "new": true}); err != nil {
		t.Fatal(err)
	}
	got, _ := q.Get("a")
	if _, ok := got.Meta["gone"]; ok || got.Meta["k"] != "v" || got.Meta["new"] != true {
		t.Fatalf("meta = %+v", got.Meta)
	}
	// Deleting from a task with no meta is a no-op, not a panic.
	if err := q.Add(&Task{ID: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetMeta("b", map[string]any{"gone": nil}); err != nil {
		t.Fatal(err)
	}
}

// Retries signal completion more often than there are tasks. A fast retry must
// not leave a finished goroutine blocked after the dispatch loop has exited,
// which would hang Run in wg.Wait forever.
func TestRun_FastRetriesDoNotHang(t *testing.T) {
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for i := 0; i < 20; i++ {
			var seen sync.Map
			o, err := NewOrchestrator(OrchestratorConfig{
				Executor: ExecutorFunc(func(_ context.Context, task *Task, _ *SharedMemory) (string, error) {
					if _, again := seen.LoadOrStore(task.ID, true); !again {
						return "", errors.New("flaky")
					}
					return "ok", nil
				}),
				MaxParallel: 1,
			})
			if err != nil {
				t.Error(err)
				return
			}
			tasks := make([]*Task, 50)
			for j := range tasks {
				tasks[j] = &Task{ID: fmt.Sprintf("t%d", j), AgentType: "general", MaxAttempts: 2}
			}
			res, err := o.Run(context.Background(), "goal", tasks)
			if err != nil {
				t.Error(err)
				return
			}
			if res.Completed != len(tasks) {
				t.Errorf("completed = %d, want %d", res.Completed, len(tasks))
				return
			}
		}
	}()
	select {
	case <-finished:
	case <-time.After(60 * time.Second):
		t.Fatal("Run hung after a fast retry")
	}
}

func TestRun_BoundedRetryExhausts(t *testing.T) {
	var calls atomic.Int32
	o, err := NewOrchestrator(OrchestratorConfig{
		Executor: ExecutorFunc(func(_ context.Context, _ *Task, _ *SharedMemory) (string, error) {
			calls.Add(1)
			return "", errors.New("always")
		}),
		MaxParallel: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := o.Run(context.Background(), "goal", []*Task{
		{ID: "a", Name: "fail", AgentType: "general", MaxAttempts: 2, VerificationCriteria: "nonempty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || res.Failed != 1 {
		t.Fatalf("calls=%d failed=%d want 2/1", calls.Load(), res.Failed)
	}
	if res.Tasks[0].VerificationCriteria != "nonempty" {
		t.Errorf("verification criteria not preserved: %+v", res.Tasks[0])
	}
}
