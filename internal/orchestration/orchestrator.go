package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Executor runs a single task for a given agent type, returning its result.
// The orchestrator supplies the shared memory so an executor can read upstream
// results and write its own. Satisfied by an adapter over the coordinator.
type Executor interface {
	Execute(ctx context.Context, task *Task, mem *SharedMemory) (string, error)
}

// ExecutorFunc adapts a function to the Executor interface.
type ExecutorFunc func(ctx context.Context, task *Task, mem *SharedMemory) (string, error)

// Execute calls the wrapped function.
func (f ExecutorFunc) Execute(ctx context.Context, task *Task, mem *SharedMemory) (string, error) {
	return f(ctx, task, mem)
}

// OrchestratorConfig configures a run.
type OrchestratorConfig struct {
	Executor    Executor      // runs each task (required)
	MaxParallel int           // concurrent tasks (default 4)
	TaskTimeout time.Duration // per-task timeout (default 5m; 0 = none)
	Progress    func(string)  // optional step progress
}

// RunResult summarises a completed orchestration.
type RunResult struct {
	Goal      string         `json:"goal"`
	Tasks     []*Task        `json:"tasks"`
	Completed int            `json:"completed"`
	Failed    int            `json:"failed"`
	Memory    map[string]any `json:"memory"`
	Duration  time.Duration  `json:"duration"`
}

// Orchestrator runs a task DAG: it claims ready tasks and dispatches them to the
// executor with bounded concurrency, recording results in shared memory until
// every task reaches a terminal state.
type Orchestrator struct {
	cfg OrchestratorConfig
	mem *SharedMemory
}

// NewOrchestrator constructs an orchestrator. The executor is required.
func NewOrchestrator(cfg OrchestratorConfig) (*Orchestrator, error) {
	if cfg.Executor == nil {
		return nil, fmt.Errorf("orchestration: executor required")
	}
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = 4
	}
	if cfg.TaskTimeout == 0 {
		cfg.TaskTimeout = 5 * time.Minute
	}
	return &Orchestrator{cfg: cfg, mem: NewSharedMemory()}, nil
}

// Memory exposes the shared memory (e.g. for inspection after a run).
func (o *Orchestrator) Memory() *SharedMemory { return o.mem }

// Run loads tasks into a queue and executes them to completion (or until the
// context is cancelled). It returns when no further progress is possible.
func (o *Orchestrator) Run(ctx context.Context, goal string, tasks []*Task) (*RunResult, error) {
	q := NewTaskQueue()
	for _, t := range tasks {
		if err := q.Add(t); err != nil {
			return nil, err
		}
	}
	// Validate up front: unknown dependency IDs and cycles both fail fast here
	// rather than silently stranding tasks mid-run (production audit M6).
	if err := q.Validate(); err != nil {
		return nil, err
	}

	start := time.Now()
	o.emit(fmt.Sprintf("🎯 Orchestrating %d tasks for: %s", len(tasks), goal))

	sem := make(chan struct{}, o.cfg.MaxParallel)
	var wg sync.WaitGroup
	// running counts in-flight task goroutines; done signals when one finishes
	// so the dispatch loop can re-check for newly-ready tasks.
	var running int
	done := make(chan struct{}, len(tasks))

	for {
		if ctx.Err() != nil {
			break
		}
		// Dispatch every currently-ready task.
		for {
			task := q.Claim()
			if task == nil {
				break
			}
			running++
			wg.Add(1)
			sem <- struct{}{}
			go func(t *Task) {
				defer wg.Done()
				defer func() { <-sem; done <- struct{}{} }()
				o.runTask(ctx, q, t)
			}(task)
		}

		if q.Done() {
			break
		}
		if running == 0 {
			// Nothing ready and nothing in flight: remaining tasks are blocked by
			// failed dependencies. Drain them (Claim marks failed-dep tasks failed).
			if !drainBlocked(q) {
				break
			}
			continue
		}
		// Wait for an in-flight task to finish, then re-check readiness.
		select {
		case <-done:
			running--
		case <-ctx.Done():
		}
	}
	wg.Wait()

	return o.summarize(goal, q, time.Since(start)), nil
}

// runTask executes one task and records its result + memory.
func (o *Orchestrator) runTask(ctx context.Context, q *TaskQueue, t *Task) {
	o.emit(fmt.Sprintf("▶️  %s (%s)", t.Name, t.AgentType))

	runCtx := ctx
	if o.cfg.TaskTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, o.cfg.TaskTimeout)
		defer cancel()
	}

	result, err := o.cfg.Executor.Execute(runCtx, t, o.mem)
	if err != nil {
		_ = q.Fail(t.ID, err.Error())
		o.emit(fmt.Sprintf("❌ %s: %v", t.Name, err))
		return
	}
	o.mem.Set("task:"+t.ID, result, t.ID)
	_ = q.Complete(t.ID, result)
	o.emit(fmt.Sprintf("✅ %s", t.Name))
}

// summarize builds the RunResult from the final queue state.
func (o *Orchestrator) summarize(goal string, q *TaskQueue, dur time.Duration) *RunResult {
	tasks := q.Tasks()
	stats := q.Stats()
	mem := map[string]any{}
	for k, e := range o.mem.Snapshot() {
		mem[k] = e.Value
	}
	o.emit(fmt.Sprintf("🏁 Done: %d completed, %d failed", stats[StateComplete], stats[StateFailed]))
	return &RunResult{
		Goal: goal, Tasks: tasks,
		Completed: stats[StateComplete], Failed: stats[StateFailed],
		Memory: mem, Duration: dur,
	}
}

// Summary renders a human-readable summary of a run.
func (r *RunResult) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n%d tasks: %d completed, %d failed (%s)\n",
		r.Goal, len(r.Tasks), r.Completed, r.Failed, r.Duration.Round(time.Millisecond))
	for _, t := range r.Tasks {
		mark := "✅"
		if t.State == StateFailed {
			mark = "❌"
		}
		fmt.Fprintf(&b, "  %s %s", mark, t.Name)
		if t.Error != "" {
			fmt.Fprintf(&b, " — %s", t.Error)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (o *Orchestrator) emit(s string) {
	if o.cfg.Progress != nil {
		o.cfg.Progress(s)
	}
}

// drainBlocked advances tasks blocked by failed dependencies by claiming once
// (Claim marks failed-dep tasks failed). Returns true if it changed any state.
func drainBlocked(q *TaskQueue) bool {
	before := q.Stats()[StateFailed]
	_ = q.Claim() // marks at most one failed-dep task as failed
	return q.Stats()[StateFailed] > before
}
