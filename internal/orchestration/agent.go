package orchestration

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vortex-run/vortex/internal/agents"
)

// AgentRouter dispatches a task to a specialized agent by type, returning its
// reply. start.go implements it over the coordinator (research/devops/pipeline/
// general handlers), keeping orchestration decoupled from those packages.
type AgentRouter interface {
	Dispatch(ctx context.Context, agentType, input string) (string, error)
}

// AgentRouterFunc adapts a function to AgentRouter.
type AgentRouterFunc func(ctx context.Context, agentType, input string) (string, error)

// Dispatch calls the wrapped function.
func (f AgentRouterFunc) Dispatch(ctx context.Context, agentType, input string) (string, error) {
	return f(ctx, agentType, input)
}

// OrchestrationAgent decomposes a goal (planner) and runs the resulting task DAG
// (orchestrator), dispatching each task to a specialized agent via the router.
//
//nolint:revive // OrchestrationAgent name is mandated by the M18 spec
type OrchestrationAgent struct {
	planner  *Planner
	router   AgentRouter
	notifier Notifier
	metrics  Metrics   // optional; wired via SetMetrics (production audit M8)
	store    *RunStore // optional; wired via SetStore (production audit H3)
}

// Notifier delivers orchestration results. Satisfied by *messaging.Router via an
// adapter; nil-safe at call sites.
type Notifier interface {
	Notify(ctx context.Context, title, body string) error
}

// NewOrchestrationAgent constructs the agent. router is required; gateway powers
// the planner; notifier is optional.
func NewOrchestrationAgent(gateway agents.AIGateway, router AgentRouter, notifier Notifier) *OrchestrationAgent {
	return &OrchestrationAgent{
		planner:  NewPlanner(gateway),
		router:   router,
		notifier: notifier,
	}
}

// SetMetrics wires the orchestration metrics sink (production audit M8). Pass
// nil to disable emission. Safe to call once at setup before any Run.
func (a *OrchestrationAgent) SetMetrics(m Metrics) { a.metrics = m }

// SetStore wires the durable run store (production audit H3): each Run's DAG
// and task transitions persist so interrupted runs resume on the next boot.
// Pass nil to disable persistence. Call once at setup before any Run.
func (a *OrchestrationAgent) SetStore(s *RunStore) { a.store = s }

// Run decomposes goal into tasks and executes them, streaming progress. It
// returns a human-readable summary.
func (a *OrchestrationAgent) Run(ctx context.Context, goal string, progressFn func(string)) (string, error) {
	if a.router == nil {
		return "", fmt.Errorf("orchestration: no agent router configured")
	}
	emit := func(s string) {
		if progressFn != nil {
			progressFn(s)
		}
	}

	emit("📋 Planning…")
	tasks, err := a.planner.Plan(ctx, goal)
	if err != nil {
		return "", fmt.Errorf("orchestration: planning: %w", err)
	}
	emit(fmt.Sprintf("📋 Plan: %d tasks", len(tasks)))

	// Persist the planned DAG before executing (production audit H3). On store
	// failure the run still executes — just without crash recovery.
	runID := ""
	if a.store != nil {
		id, cerr := a.store.CreateRun(goal, "", tasks)
		if cerr != nil {
			slog.Default().Warn("orchestration: recording run (persistence disabled for this run)", "err", cerr)
		} else {
			runID = id
		}
	}

	orch, err := NewOrchestrator(OrchestratorConfig{
		Executor:    a.executor(),
		MaxParallel: 4,
		Progress:    progressFn,
		Metrics:     a.metrics,
		Store:       a.store,
		RunID:       runID,
	})
	if err != nil {
		return "", err
	}

	result, err := orch.Run(ctx, goal, tasks)
	if err != nil {
		return "", err
	}

	a.finishRun(runID, result)
	summary := result.Summary()
	a.notify(ctx, goal, result)
	return summary, nil
}

// finishRun marks the persisted run terminal — but only when every task
// reached a terminal state. A ctx-cancelled run (graceful shutdown) leaves
// tasks pending; its record stays "running" so the next boot resumes it
// rather than mislabeling the interruption a failure (production audit H3).
func (a *OrchestrationAgent) finishRun(runID string, result *RunResult) {
	if a.store == nil || runID == "" {
		return
	}
	if result.Completed+result.Failed != len(result.Tasks) {
		return // interrupted — leave status running, resumable
	}
	status := RunDone
	if result.Failed > 0 {
		status = RunFailed
	}
	if err := a.store.FinishRun(runID, status, result.Summary()); err != nil {
		slog.Default().Warn("orchestration: finishing run record", "run", runID, "err", err)
	}
}

// maxResumeAttempts caps how many boots may resume the same run, so a poison
// task that crashes the process cannot produce an infinite crash-resume loop.
const maxResumeAttempts = 3

// ResumeIncomplete resumes runs a previous process left "running" (crash or
// hard shutdown), sequentially oldest-first (production audit H3). Completed
// tasks keep their persisted results and are never re-executed (exactly-once);
// tasks that were mid-flight re-run (at-least-once); failed tasks stay failed.
// Call once at boot, typically in a background goroutine.
func (a *OrchestrationAgent) ResumeIncomplete(ctx context.Context, progressFn func(string)) {
	if a.store == nil {
		return
	}
	runs, err := a.store.ListIncomplete()
	if err != nil {
		slog.Default().Warn("orchestration: listing interrupted runs", "err", err)
		return
	}
	if len(runs) > 0 {
		slog.Default().Info("orchestration: resuming interrupted runs", "count", len(runs))
	}
	for _, run := range runs {
		if ctx.Err() != nil {
			return
		}
		a.resumeRun(ctx, run, progressFn)
	}
}

// resumeRun rehydrates and continues one interrupted run. Panics are contained
// so a poisoned run cannot stop the remaining runs from resuming.
func (a *OrchestrationAgent) resumeRun(ctx context.Context, run RunInfo, progressFn func(string)) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Error("orchestration: resume panicked", "run", run.ID, "panic", rec)
		}
	}()

	n, err := a.store.MarkResumed(run.ID)
	if err != nil {
		slog.Default().Warn("orchestration: marking run resumed", "run", run.ID, "err", err)
		return
	}
	if n > maxResumeAttempts {
		reason := fmt.Sprintf("exceeded %d resume attempts", maxResumeAttempts)
		if ferr := a.store.FinishRun(run.ID, RunFailed, reason); ferr != nil {
			slog.Default().Warn("orchestration: failing exhausted run", "run", run.ID, "err", ferr)
		}
		slog.Default().Warn("orchestration: abandoning run", "run", run.ID, "goal", run.Goal, "reason", reason)
		return
	}

	tasks, memory, err := a.store.LoadRun(run.ID)
	if err != nil {
		slog.Default().Warn("orchestration: loading interrupted run", "run", run.ID, "err", err)
		return
	}
	if len(tasks) == 0 {
		_ = a.store.FinishRun(run.ID, RunFailed, "no tasks persisted")
		return
	}
	// Interrupted work re-runs (at-least-once): anything mid-flight at the
	// crash goes back to pending with its partial output cleared. Completed
	// tasks keep state+result and are skipped by Claim; failed stays failed.
	for _, t := range tasks {
		if t.State == StateRunning || t.State == StateReady {
			t.State = StatePending
			t.Result, t.Error = "", ""
			t.StartedAt, t.FinishedAt = time.Time{}, time.Time{}
		}
	}

	orch, err := NewOrchestrator(OrchestratorConfig{
		Executor:    a.executor(),
		MaxParallel: 4,
		Progress:    progressFn,
		Metrics:     a.metrics,
		Store:       a.store,
		RunID:       run.ID,
	})
	if err != nil {
		slog.Default().Warn("orchestration: building resume orchestrator", "run", run.ID, "err", err)
		return
	}
	// Seed shared memory from the persisted snapshot, then backfill task:<id>
	// keys from completed results — buildInput feeds dependents from them, so
	// a missing key silently degrades resumed dependents' context.
	for _, e := range memory {
		orch.Memory().Set(e.Key, e.Value, e.Author)
	}
	for _, t := range tasks {
		if t.State == StateComplete && !orch.Memory().Has("task:"+t.ID) {
			orch.Memory().Set("task:"+t.ID, t.Result, t.ID)
		}
	}

	result, err := orch.Run(ctx, run.Goal, tasks)
	if err != nil {
		slog.Default().Warn("orchestration: resumed run failed to start", "run", run.ID, "err", err)
		return
	}
	a.finishRun(run.ID, result)
	a.notify(ctx, run.Goal, result)
}

// executor builds an Executor that dispatches each task to its agent, injecting
// upstream dependency results from shared memory into the task input.
func (a *OrchestrationAgent) executor() Executor {
	return ExecutorFunc(func(ctx context.Context, t *Task, mem *SharedMemory) (string, error) {
		input := a.buildInput(t, mem)
		agentType := t.AgentType
		if agentType == "" {
			agentType = "general"
		}
		return a.router.Dispatch(ctx, agentType, input)
	})
}

// buildInput prepends upstream task results (from shared memory) to the task's
// own input so the agent has the context it depends on.
func (a *OrchestrationAgent) buildInput(t *Task, mem *SharedMemory) string {
	if len(t.DependsOn) == 0 {
		return t.Input
	}
	var b strings.Builder
	b.WriteString("Context from previous steps:\n")
	for _, dep := range t.DependsOn {
		if v := mem.GetString("task:" + dep); v != "" {
			fmt.Fprintf(&b, "- %s\n", truncate(v, 2000))
		}
	}
	b.WriteString("\nTask: ")
	b.WriteString(t.Input)
	return b.String()
}

// notify sends the run summary to the notifier if configured.
func (a *OrchestrationAgent) notify(ctx context.Context, goal string, result *RunResult) {
	if a.notifier == nil {
		return
	}
	body := fmt.Sprintf("🤖 Orchestration complete: %s\n%d completed, %d failed",
		goal, result.Completed, result.Failed)
	_ = a.notifier.Notify(ctx, "VORTEX Orchestration", body)
}

// truncate caps a string to n runes.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
