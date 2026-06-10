package orchestration

import (
	"context"
	"fmt"
	"strings"

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

	orch, err := NewOrchestrator(OrchestratorConfig{
		Executor:    a.executor(),
		MaxParallel: 4,
		Progress:    progressFn,
	})
	if err != nil {
		return "", err
	}

	result, err := orch.Run(ctx, goal, tasks)
	if err != nil {
		return "", err
	}

	summary := result.Summary()
	a.notify(ctx, goal, result)
	return summary, nil
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
