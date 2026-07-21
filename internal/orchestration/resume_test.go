package orchestration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingRouter records Dispatch calls per task input and returns canned
// replies, standing in for the real agent router during resume tests.
type countingRouter struct {
	mu     sync.Mutex
	calls  map[string]int    // agentType -> count
	inputs map[string]string // agentType -> last input seen
	fail   map[string]error  // agentType -> error to return
}

func newCountingRouter() *countingRouter {
	return &countingRouter{calls: map[string]int{}, inputs: map[string]string{}, fail: map[string]error{}}
}

func (r *countingRouter) Dispatch(_ context.Context, agentType, input string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[agentType]++
	r.inputs[agentType] = input
	if err := r.fail[agentType]; err != nil {
		return "", err
	}
	return "out:" + agentType, nil
}

func (r *countingRouter) count(agentType string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[agentType]
}

func (r *countingRouter) lastInput(agentType string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inputs[agentType]
}

// resumeAgent builds an OrchestrationAgent wired for resume tests: counting
// router, no gateway (the planner is unused on resume), given store.
func resumeAgent(store *RunStore, router AgentRouter) *OrchestrationAgent {
	a := NewOrchestrationAgent(nil, router, nil)
	a.SetStore(store)
	return a
}

// TestResume_SkipsCompletedReplaysInterrupted is the core crash simulation:
// chain a→b→c persisted with a complete (result "ra"), b mid-flight (running),
// c pending. A fresh process resumes: a must NOT re-execute (exactly-once), b
// and c run once each, and b's input carries a's persisted result.
func TestResume_SkipsCompletedReplaysInterrupted(t *testing.T) {
	s := newTestStore(t)
	tasks := []*Task{
		{ID: "a", Name: "A", AgentType: "research", Input: "find"},
		{ID: "b", Name: "B", AgentType: "devops", Input: "deploy", DependsOn: []string{"a"}},
		{ID: "c", Name: "C", AgentType: "general", Input: "report", DependsOn: []string{"b"}},
	}
	runID, err := s.CreateRun("crash sim", "", tasks)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate pre-crash progress: a completed, b was running.
	if err := s.SaveTask(runID, &Task{ID: "a", State: StateComplete, Result: "ra"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTask(runID, &Task{ID: "b", State: StateRunning, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// No SyncMemory on purpose: the resume must backfill task:a from a's
	// persisted Result.

	router := newCountingRouter()
	resumeAgent(s, router).ResumeIncomplete(context.Background(), nil)

	if n := router.count("research"); n != 0 {
		t.Errorf("completed task a re-executed %d times, want 0 (exactly-once)", n)
	}
	if n := router.count("devops"); n != 1 {
		t.Errorf("interrupted task b executed %d times, want 1", n)
	}
	if n := router.count("general"); n != 1 {
		t.Errorf("pending task c executed %d times, want 1", n)
	}
	if in := router.lastInput("devops"); !strings.Contains(in, "ra") {
		t.Errorf("b's input %q missing upstream result %q (memory backfill)", in, "ra")
	}

	// The run must now be terminal (done) with all task rows complete.
	if runs, _ := s.ListIncomplete(); len(runs) != 0 {
		t.Errorf("run still incomplete after resume: %+v", runs)
	}
	loaded, _, _ := s.LoadRun(runID)
	for _, tk := range loaded {
		if tk.State != StateComplete {
			t.Errorf("task %s state = %s, want complete", tk.ID, tk.State)
		}
	}
}

// TestResume_AllTerminalFinishesWithoutExecution covers a crash between the
// last task completing and FinishRun: resume must mark the run done without
// executing anything.
func TestResume_AllTerminalFinishesWithoutExecution(t *testing.T) {
	s := newTestStore(t)
	tasks := []*Task{
		{ID: "a", AgentType: "research", State: StateComplete, Result: "ra"},
		{ID: "b", AgentType: "devops", State: StateComplete, Result: "rb", DependsOn: []string{"a"}},
	}
	runID, _ := s.CreateRun("done but unfinished", "", tasks)

	router := newCountingRouter()
	resumeAgent(s, router).ResumeIncomplete(context.Background(), nil)

	if n := router.count("research") + router.count("devops"); n != 0 {
		t.Errorf("executor called %d times on all-terminal resume, want 0", n)
	}
	if runs, _ := s.ListIncomplete(); len(runs) != 0 {
		t.Errorf("all-terminal run not finished: %+v", runs)
	}
	var status string
	_ = s.db.QueryRow(`SELECT status FROM orch_runs WHERE id=?`, runID).Scan(&status)
	if status != RunDone {
		t.Errorf("run status = %s, want done", status)
	}
}

// TestResume_FailedTaskStaysFailedDependentsFail: a persisted-failed task is
// terminal; its dependents fail without execution while independent branches
// still run.
func TestResume_FailedTaskStaysFailedDependentsFail(t *testing.T) {
	s := newTestStore(t)
	tasks := []*Task{
		{ID: "a", AgentType: "research", State: StateFailed, Error: "boom"},
		{ID: "b", AgentType: "devops", DependsOn: []string{"a"}},
		{ID: "d", AgentType: "general", Input: "independent"},
	}
	runID, _ := s.CreateRun("partial failure", "", tasks)

	router := newCountingRouter()
	resumeAgent(s, router).ResumeIncomplete(context.Background(), nil)

	if n := router.count("research"); n != 0 {
		t.Errorf("failed task a re-executed %d times, want 0 (failure is terminal)", n)
	}
	if n := router.count("devops"); n != 0 {
		t.Errorf("dependent b executed %d times, want 0", n)
	}
	if n := router.count("general"); n != 1 {
		t.Errorf("independent d executed %d times, want 1", n)
	}
	var status string
	_ = s.db.QueryRow(`SELECT status FROM orch_runs WHERE id=?`, runID).Scan(&status)
	if status != RunFailed {
		t.Errorf("run status = %s, want failed", status)
	}
	// Dep-failure reconcile: b was marked failed inside Claim (never entered
	// runTask); the end-of-run SyncTasks must still have persisted it.
	loaded, _, _ := s.LoadRun(runID)
	for _, tk := range loaded {
		if tk.ID == "b" && tk.State != StateFailed {
			t.Errorf("dependent b DB state = %s, want failed (SyncTasks reconcile)", tk.State)
		}
	}
}

// TestResume_CapAbandonsPoisonRun: a run already resumed maxResumeAttempts
// times is marked failed without executing anything (crash-loop breaker).
func TestResume_CapAbandonsPoisonRun(t *testing.T) {
	s := newTestStore(t)
	runID, _ := s.CreateRun("poison", "", []*Task{{ID: "a", AgentType: "research"}})
	for i := 0; i < maxResumeAttempts; i++ {
		if _, err := s.MarkResumed(runID); err != nil {
			t.Fatal(err)
		}
	}

	router := newCountingRouter()
	resumeAgent(s, router).ResumeIncomplete(context.Background(), nil)

	if n := router.count("research"); n != 0 {
		t.Errorf("poison run executed %d tasks, want 0", n)
	}
	var status, result string
	_ = s.db.QueryRow(`SELECT status, result FROM orch_runs WHERE id=?`, runID).Scan(&status, &result)
	if status != RunFailed || !strings.Contains(result, "resume attempts") {
		t.Errorf("poison run status/result = %s/%q, want failed/…resume attempts…", status, result)
	}
}

// TestRun_CtxCancelLeavesRunResumable: a graceful shutdown (ctx cancel) mid-run
// must NOT mark the run terminal — it stays "running" for the next boot.
func TestRun_CtxCancelLeavesRunResumable(t *testing.T) {
	s := newTestStore(t)
	tasks := []*Task{
		{ID: "a", AgentType: "research"},
		{ID: "b", AgentType: "devops", DependsOn: []string{"a"}},
	}
	runID, _ := s.CreateRun("interrupted", "", tasks)

	ctx, cancel := context.WithCancel(context.Background())
	exec := ExecutorFunc(func(_ context.Context, _ *Task, _ *SharedMemory) (string, error) {
		cancel() // first task cancels the run mid-flight
		return "ra", nil
	})
	o := newOrch(t, exec, func(c *OrchestratorConfig) { c.Store = s; c.RunID = runID })
	result, err := o.Run(ctx, "interrupted", tasks)
	if err != nil {
		t.Fatal(err)
	}
	if result.Completed+result.Failed == len(result.Tasks) {
		t.Fatalf("expected an interrupted run, got all-terminal: %+v", result)
	}

	agent := resumeAgent(s, nil)
	agent.finishRun(runID, result)

	runs, _ := s.ListIncomplete()
	if len(runs) != 1 || runs[0].ID != runID {
		t.Errorf("interrupted run not resumable: incomplete=%+v", runs)
	}
}

// TestRun_StoreErrorsAreNonFatal: persistence failures (closed DB) must never
// fail the run itself.
func TestRun_StoreErrorsAreNonFatal(t *testing.T) {
	s := newTestStore(t)
	runID, _ := s.CreateRun("g", "", []*Task{{ID: "a"}})
	_ = s.Close() // every subsequent store call errors

	exec := ExecutorFunc(func(_ context.Context, _ *Task, _ *SharedMemory) (string, error) {
		return "ok", nil
	})
	o := newOrch(t, exec, func(c *OrchestratorConfig) { c.Store = s; c.RunID = runID })
	result, err := o.Run(context.Background(), "g", []*Task{{ID: "a"}})
	if err != nil {
		t.Fatalf("run failed because of store errors: %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("completed = %d, want 1", result.Completed)
	}
}

// TestResume_RouterErrorMarksRunFailed: a resumed task whose agent errors
// yields a failed (terminal) run, not an infinite resume loop.
func TestResume_RouterErrorMarksRunFailed(t *testing.T) {
	s := newTestStore(t)
	runID, _ := s.CreateRun("errors", "", []*Task{{ID: "a", AgentType: "research"}})

	router := newCountingRouter()
	router.fail["research"] = errors.New("agent unavailable")
	resumeAgent(s, router).ResumeIncomplete(context.Background(), nil)

	var status string
	_ = s.db.QueryRow(`SELECT status FROM orch_runs WHERE id=?`, runID).Scan(&status)
	if status != RunFailed {
		t.Errorf("run status = %s, want failed", status)
	}
}
