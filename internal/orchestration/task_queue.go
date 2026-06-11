// Package orchestration implements VORTEX's multi-agent orchestration (build
// plan M18): decomposing a goal into tasks (planner), running them with
// dependency + concurrency control (orchestrator), a shared key/value memory,
// and a dependency-aware task queue. It is stdlib-only.
//
// This file implements the task queue.
package orchestration

import (
	"fmt"
	"sync"
	"time"
)

// TaskState is a task's lifecycle state.
type TaskState string

const (
	StatePending  TaskState = "pending"  // not yet eligible (deps unmet) or waiting
	StateReady    TaskState = "ready"    // deps satisfied, awaiting a worker
	StateRunning  TaskState = "running"  // claimed by a worker
	StateComplete TaskState = "complete" // finished successfully
	StateFailed   TaskState = "failed"   // finished with an error
)

// Task is a unit of work with dependencies.
type Task struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	AgentType string         `json:"agent_type"` // which agent should run it
	Input     string         `json:"input"`      // the instruction/prompt
	DependsOn []string       `json:"depends_on"` // task IDs that must complete first
	State     TaskState      `json:"state"`
	Result    string         `json:"result,omitempty"`
	Error     string         `json:"error,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`

	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// TaskQueue is a dependency-aware queue: a task becomes ready only once all of
// its DependsOn tasks are complete. Safe for concurrent use.
type TaskQueue struct {
	mu    sync.Mutex
	tasks map[string]*Task
	order []string // insertion order for stable iteration
}

// NewTaskQueue constructs an empty queue.
func NewTaskQueue() *TaskQueue {
	return &TaskQueue{tasks: map[string]*Task{}}
}

// Add registers a task. The ID must be unique and non-empty; dependencies need
// not exist yet (they're resolved at claim time) but must exist before the task
// can run. Returns an error on duplicate or empty ID.
func (q *TaskQueue) Add(t *Task) error {
	if t == nil || t.ID == "" {
		return fmt.Errorf("orchestration: task ID required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, exists := q.tasks[t.ID]; exists {
		return fmt.Errorf("orchestration: duplicate task ID %q", t.ID)
	}
	if t.State == "" {
		t.State = StatePending
	}
	cp := *t
	q.tasks[t.ID] = &cp
	q.order = append(q.order, t.ID)
	return nil
}

// Claim returns the next ready task (deps all complete), marking it running, or
// nil if none are ready. A task whose dependency failed is itself marked failed
// and skipped.
func (q *TaskQueue) Claim() *Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, id := range q.order {
		t := q.tasks[id]
		if t.State != StatePending && t.State != StateReady {
			continue
		}
		dep := q.depStatus(t)
		switch dep {
		case depReady:
			t.State = StateRunning
			t.StartedAt = time.Now()
			cp := *t
			return &cp
		case depFailed:
			t.State = StateFailed
			t.Error = "dependency failed"
			t.FinishedAt = time.Now()
		case depWaiting:
			t.State = StatePending
		}
	}
	return nil
}

// dependency resolution outcomes.
type depResult int

const (
	depReady depResult = iota
	depWaiting
	depFailed
)

// depStatus reports whether t's dependencies are satisfied/failed/pending.
func (q *TaskQueue) depStatus(t *Task) depResult {
	for _, dep := range t.DependsOn {
		d, ok := q.tasks[dep]
		if !ok {
			// Unknown dependency: treat as failed rather than waiting forever.
			// Run() calls Validate() up front so this should be unreachable in
			// the normal path, but if a task is added after validation this
			// prevents a silent strand (production audit M6).
			return depFailed
		}
		switch d.State {
		case StateFailed:
			return depFailed
		case StateComplete:
			continue
		default:
			return depWaiting
		}
	}
	return depReady
}

// Complete marks a task complete with its result.
func (q *TaskQueue) Complete(id, result string) error {
	return q.finish(id, StateComplete, result, "")
}

// Fail marks a task failed with an error message.
func (q *TaskQueue) Fail(id, errMsg string) error {
	return q.finish(id, StateFailed, "", errMsg)
}

func (q *TaskQueue) finish(id string, state TaskState, result, errMsg string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.tasks[id]
	if !ok {
		return fmt.Errorf("orchestration: no task %q", id)
	}
	t.State = state
	t.Result = result
	t.Error = errMsg
	t.FinishedAt = time.Now()
	return nil
}

// Get returns a copy of a task by ID.
func (q *TaskQueue) Get(id string) (*Task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.tasks[id]
	if !ok {
		return nil, false
	}
	cp := *t
	return &cp, true
}

// Tasks returns copies of all tasks in insertion order.
func (q *TaskQueue) Tasks() []*Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]*Task, 0, len(q.order))
	for _, id := range q.order {
		cp := *q.tasks[id]
		out = append(out, &cp)
	}
	return out
}

// Done reports whether every task has reached a terminal state.
func (q *TaskQueue) Done() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, t := range q.tasks {
		if t.State != StateComplete && t.State != StateFailed {
			return false
		}
	}
	return true
}

// Stats returns counts by state.
func (q *TaskQueue) Stats() map[TaskState]int {
	q.mu.Lock()
	defer q.mu.Unlock()
	counts := map[TaskState]int{}
	for _, t := range q.tasks {
		counts[t.State]++
	}
	return counts
}

// Validate checks the task graph is runnable before execution: every
// DependsOn ID must reference a task that exists, and the graph must be
// acyclic. An unknown dependency would otherwise leave the dependent task
// stranded in "pending" forever — the run loop would exit and report it as
// neither complete nor failed, silently skipping work (production audit M6).
// Returns a descriptive error naming the first problem, or nil when runnable.
func (q *TaskQueue) Validate() error {
	q.mu.Lock()
	for _, id := range q.order {
		t := q.tasks[id]
		for _, dep := range t.DependsOn {
			if _, ok := q.tasks[dep]; !ok {
				q.mu.Unlock()
				return fmt.Errorf("orchestration: task %q depends on unknown task %q", t.ID, dep)
			}
		}
	}
	q.mu.Unlock()

	if q.HasCycle() {
		return fmt.Errorf("orchestration: task graph has a cycle")
	}
	return nil
}

// HasCycle reports whether the dependency graph contains a cycle (which would
// deadlock the queue). Uses DFS with a visiting set.
func (q *TaskQueue) HasCycle() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	const (
		white = 0 // unvisited
		gray  = 1 // on the current DFS stack
		black = 2 // fully explored
	)
	color := make(map[string]int, len(q.tasks))
	var visit func(id string) bool
	visit = func(id string) bool {
		color[id] = gray
		t, ok := q.tasks[id]
		if ok {
			for _, dep := range t.DependsOn {
				switch color[dep] {
				case gray:
					return true // back-edge → cycle
				case white:
					if _, exists := q.tasks[dep]; exists && visit(dep) {
						return true
					}
				}
			}
		}
		color[id] = black
		return false
	}
	for _, id := range q.order {
		if color[id] == white {
			if visit(id) {
				return true
			}
		}
	}
	return false
}
