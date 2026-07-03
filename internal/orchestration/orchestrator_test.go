package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newOrch(t *testing.T, exec Executor, cfg ...func(*OrchestratorConfig)) *Orchestrator {
	t.Helper()
	c := OrchestratorConfig{Executor: exec, MaxParallel: 4, TaskTimeout: 2 * time.Second}
	for _, f := range cfg {
		f(&c)
	}
	o, err := NewOrchestrator(c)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestNewOrchestrator_RequiresExecutor(t *testing.T) {
	if _, err := NewOrchestrator(OrchestratorConfig{}); err == nil {
		t.Error("missing executor should error")
	}
}

func TestRun_ExecutesAllTasks(t *testing.T) {
	var count atomic.Int32
	exec := ExecutorFunc(func(_ context.Context, t *Task, _ *SharedMemory) (string, error) {
		count.Add(1)
		return "done:" + t.ID, nil
	})
	o := newOrch(t, exec)
	res, err := o.Run(context.Background(), "goal", []*Task{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if count.Load() != 3 || res.Completed != 3 || res.Failed != 0 {
		t.Errorf("run result = %+v (count=%d)", res, count.Load())
	}
}

func TestRun_RespectsDependencyOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	exec := ExecutorFunc(func(_ context.Context, t *Task, _ *SharedMemory) (string, error) {
		mu.Lock()
		order = append(order, t.ID)
		mu.Unlock()
		return "", nil
	})
	o := newOrch(t, exec)
	_, err := o.Run(context.Background(), "g", []*Task{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// a before b before c.
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Errorf("execution order = %v, want [a b c]", order)
	}
}

func TestRun_PassesResultsViaMemory(t *testing.T) {
	exec := ExecutorFunc(func(_ context.Context, t *Task, mem *SharedMemory) (string, error) {
		if t.ID == "consumer" {
			// Should be able to read the producer's result.
			if got := mem.GetString("task:producer"); got != "produced-value" {
				return "", fmt.Errorf("consumer saw %q", got)
			}
		}
		if t.ID == "producer" {
			return "produced-value", nil
		}
		return "ok", nil
	})
	o := newOrch(t, exec)
	res, err := o.Run(context.Background(), "g", []*Task{
		{ID: "producer"},
		{ID: "consumer", DependsOn: []string{"producer"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 {
		t.Errorf("consumer should have read the producer result: %+v", res.Tasks)
	}
}

func TestRun_FailurePropagatesToDependents(t *testing.T) {
	exec := ExecutorFunc(func(_ context.Context, t *Task, _ *SharedMemory) (string, error) {
		if t.ID == "a" {
			return "", fmt.Errorf("a failed")
		}
		return "ok", nil
	})
	o := newOrch(t, exec)
	res, err := o.Run(context.Background(), "g", []*Task{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// a fails; b is blocked → failed.
	if res.Completed != 0 || res.Failed != 2 {
		t.Errorf("expected both failed, got %+v", res)
	}
}

func TestRun_ParallelExecution(t *testing.T) {
	var concurrent, maxC atomic.Int32
	exec := ExecutorFunc(func(_ context.Context, _ *Task, _ *SharedMemory) (string, error) {
		n := concurrent.Add(1)
		for {
			m := maxC.Load()
			if n <= m || maxC.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		concurrent.Add(-1)
		return "", nil
	})
	o := newOrch(t, exec, func(c *OrchestratorConfig) { c.MaxParallel = 3 })
	tasks := make([]*Task, 6)
	for i := range tasks {
		tasks[i] = &Task{ID: fmt.Sprintf("t%d", i)}
	}
	_, err := o.Run(context.Background(), "g", tasks)
	if err != nil {
		t.Fatal(err)
	}
	if maxC.Load() < 2 {
		t.Errorf("tasks did not run in parallel (max=%d)", maxC.Load())
	}
	if maxC.Load() > 3 {
		t.Errorf("MaxParallel exceeded: max=%d", maxC.Load())
	}
}

func TestRun_RejectsCycle(t *testing.T) {
	exec := ExecutorFunc(func(context.Context, *Task, *SharedMemory) (string, error) { return "", nil })
	o := newOrch(t, exec)
	_, err := o.Run(context.Background(), "g", []*Task{
		{ID: "a", DependsOn: []string{"b"}},
		{ID: "b", DependsOn: []string{"a"}},
	})
	if err == nil {
		t.Error("a cyclic graph should error")
	}
}

func TestRun_DiamondCompletes(t *testing.T) {
	var ran sync.Map
	exec := ExecutorFunc(func(_ context.Context, t *Task, _ *SharedMemory) (string, error) {
		ran.Store(t.ID, true)
		return "", nil
	})
	o := newOrch(t, exec)
	res, err := o.Run(context.Background(), "g", []*Task{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"a"}},
		{ID: "d", DependsOn: []string{"b", "c"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Completed != 4 {
		t.Errorf("diamond should complete all 4, got %d", res.Completed)
	}
	var ids []string
	ran.Range(func(k, _ any) bool { ids = append(ids, k.(string)); return true })
	sort.Strings(ids)
	if len(ids) != 4 {
		t.Errorf("ran tasks = %v", ids)
	}
}

func TestRunResult_Summary(t *testing.T) {
	exec := ExecutorFunc(func(_ context.Context, t *Task, _ *SharedMemory) (string, error) {
		if t.ID == "b" {
			return "", fmt.Errorf("boom")
		}
		return "", nil
	})
	o := newOrch(t, exec)
	res, _ := o.Run(context.Background(), "my goal", []*Task{
		{ID: "a", Name: "Task A"}, {ID: "b", Name: "Task B"},
	})
	s := res.Summary()
	if !contains(s, "my goal") || !contains(s, "✅ Task A") || !contains(s, "❌ Task B") {
		t.Errorf("summary = %q", s)
	}
}

func TestRun_MemoryAccessible(t *testing.T) {
	exec := ExecutorFunc(func(_ context.Context, t *Task, mem *SharedMemory) (string, error) {
		mem.Set("custom:"+t.ID, "x", t.ID)
		return "r", nil
	})
	o := newOrch(t, exec)
	_, _ = o.Run(context.Background(), "g", []*Task{{ID: "a"}})
	if !o.Memory().Has("custom:a") || !o.Memory().Has("task:a") {
		t.Error("shared memory should hold both custom and task results")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// fakeMetrics is a test sink for orchestration metrics. It records every event
// so tests can assert the run counter fires once and TaskStarted/TaskFinished
// stay balanced (the active gauge cannot drift).
type fakeMetrics struct {
	mu       sync.Mutex
	runs     int
	started  int
	finished int
	outcomes map[string]int // "agentType/outcome" -> count
}

func newFakeMetrics() *fakeMetrics { return &fakeMetrics{outcomes: map[string]int{}} }

func (f *fakeMetrics) RecordOrchestrationRun() {
	f.mu.Lock()
	f.runs++
	f.mu.Unlock()
}

func (f *fakeMetrics) TaskStarted() {
	f.mu.Lock()
	f.started++
	f.mu.Unlock()
}

func (f *fakeMetrics) TaskFinished(agentType, outcome string, _ time.Duration) {
	f.mu.Lock()
	f.finished++
	f.outcomes[agentType+"/"+outcome]++
	f.mu.Unlock()
}

func TestRun_EmitsMetrics(t *testing.T) {
	exec := ExecutorFunc(func(_ context.Context, tk *Task, _ *SharedMemory) (string, error) {
		if tk.ID == "b" {
			return "", errors.New("boom")
		}
		return "ok", nil
	})
	fm := newFakeMetrics()
	o := newOrch(t, exec, func(c *OrchestratorConfig) { c.Metrics = fm })
	_, err := o.Run(context.Background(), "goal", []*Task{
		{ID: "a", AgentType: "research"},
		{ID: "b", AgentType: "devops"},
	})
	if err != nil {
		t.Fatal(err)
	}

	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.runs != 1 {
		t.Errorf("runs = %d, want 1", fm.runs)
	}
	// Every started task must finish — otherwise the in-flight gauge drifts.
	if fm.started != 2 || fm.finished != 2 {
		t.Errorf("started/finished = %d/%d, want 2/2 (gauge would drift)", fm.started, fm.finished)
	}
	if fm.outcomes["research/complete"] != 1 {
		t.Errorf("research/complete = %d, want 1", fm.outcomes["research/complete"])
	}
	if fm.outcomes["devops/failed"] != 1 {
		t.Errorf("devops/failed = %d, want 1", fm.outcomes["devops/failed"])
	}
}

func TestRun_NilMetricsIsSafe(t *testing.T) {
	exec := ExecutorFunc(func(_ context.Context, _ *Task, _ *SharedMemory) (string, error) {
		return "ok", nil
	})
	o := newOrch(t, exec) // no Metrics set
	if _, err := o.Run(context.Background(), "g", []*Task{{ID: "a"}}); err != nil {
		t.Fatal(err)
	}
}

// TestRun_MetricsBalanceOnPanic verifies a panicking executor is recovered
// (does not crash the process), is counted as a "failed" task, and does not
// leak the in-flight gauge — TaskStarted stays balanced by TaskFinished.
func TestRun_MetricsBalanceOnPanic(t *testing.T) {
	exec := ExecutorFunc(func(_ context.Context, _ *Task, _ *SharedMemory) (string, error) {
		panic("executor blew up")
	})
	fm := newFakeMetrics()
	o := newOrch(t, exec, func(c *OrchestratorConfig) { c.Metrics = fm })

	res, err := o.Run(context.Background(), "g", []*Task{{ID: "a", AgentType: "devops"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 || res.Completed != 0 {
		t.Errorf("panicking task should be failed: %+v", res)
	}

	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.started != 1 || fm.finished != 1 {
		t.Errorf("started/finished = %d/%d, want 1/1 (gauge leaked on panic)", fm.started, fm.finished)
	}
	if fm.outcomes["devops/failed"] != 1 {
		t.Errorf("panic outcome = %v, want devops/failed=1", fm.outcomes)
	}
}
