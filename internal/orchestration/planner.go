package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vortex-run/vortex/internal/agents"
)

// plannerSystemPrompt instructs the model to decompose a goal into a task DAG.
const plannerSystemPrompt = `You are a planner that decomposes a goal into a small set of tasks for specialized agents. Available agent types: research, devops, data_pipeline, build_app, general. Respond with ONLY this JSON:
{"tasks":[{"id":"t1","name":"...","agent":"research","input":"...","depends_on":[]},{"id":"t2","name":"...","agent":"general","input":"...","depends_on":["t1"]}]}
Rules: ids are unique short strings; depends_on lists ids that must finish first; keep it to 2-6 tasks; no cycles. Return only JSON.`

// maxPlanTasks caps how many tasks a plan may contain.
const maxPlanTasks = 12

// validAgentTypes are the agent types a plan may target.
var validAgentTypes = map[string]bool{
	"research": true, "devops": true, "data_pipeline": true,
	"build_app": true, "general": true,
}

// Planner decomposes a goal into a Plan (task DAG) using the AI gateway.
type Planner struct {
	gateway agents.AIGateway
}

// NewPlanner constructs a planner over an AI gateway.
func NewPlanner(gateway agents.AIGateway) *Planner {
	return &Planner{gateway: gateway}
}

// Plan decomposes goal into a validated set of tasks. The returned tasks are
// ready to load into a TaskQueue (deduplicated IDs, known agent types,
// acyclic). When AI is unavailable, it falls back to a single general task.
func (p *Planner) Plan(ctx context.Context, goal string) ([]*Task, error) {
	if strings.TrimSpace(goal) == "" {
		return nil, fmt.Errorf("orchestration: empty goal")
	}
	if p.gateway == nil {
		return p.fallback(goal), nil
	}
	reply, err := p.gateway.Complete(ctx, "Goal: "+goal, plannerSystemPrompt)
	if err != nil {
		return p.fallback(goal), nil
	}
	tasks := parsePlanTasks(reply)
	if len(tasks) == 0 {
		return p.fallback(goal), nil
	}
	if err := validatePlan(tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// fallback returns a single general task that handles the whole goal.
func (p *Planner) fallback(goal string) []*Task {
	return []*Task{{ID: "t1", Name: "Handle goal", AgentType: "general", Input: goal}}
}

// parsePlanTasks extracts tasks from a model reply (tolerant of prose).
func parsePlanTasks(reply string) []*Task {
	js := extractJSONObject(reply)
	if js == "" {
		return nil
	}
	var raw struct {
		Tasks []struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			Agent     string   `json:"agent"`
			Input     string   `json:"input"`
			DependsOn []string `json:"depends_on"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(js), &raw); err != nil {
		return nil
	}
	tasks := make([]*Task, 0, len(raw.Tasks))
	for _, r := range raw.Tasks {
		tasks = append(tasks, &Task{
			ID: r.ID, Name: r.Name, AgentType: r.Agent,
			Input: r.Input, DependsOn: r.DependsOn,
		})
	}
	return tasks
}

// validatePlan checks IDs, agent types, dependency references, and acyclicity.
func validatePlan(tasks []*Task) error {
	if len(tasks) == 0 {
		return fmt.Errorf("orchestration: plan has no tasks")
	}
	if len(tasks) > maxPlanTasks {
		return fmt.Errorf("orchestration: plan has %d tasks (max %d)", len(tasks), maxPlanTasks)
	}
	ids := map[string]bool{}
	for _, t := range tasks {
		if t.ID == "" {
			return fmt.Errorf("orchestration: task with empty ID")
		}
		if ids[t.ID] {
			return fmt.Errorf("orchestration: duplicate task ID %q", t.ID)
		}
		ids[t.ID] = true
		if t.AgentType != "" && !validAgentTypes[t.AgentType] {
			return fmt.Errorf("orchestration: task %q has unknown agent type %q", t.ID, t.AgentType)
		}
	}
	// Dependencies must reference known tasks.
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if !ids[dep] {
				return fmt.Errorf("orchestration: task %q depends on unknown task %q", t.ID, dep)
			}
		}
	}
	// Acyclicity: build a queue and reuse its cycle check.
	q := NewTaskQueue()
	for _, t := range tasks {
		if err := q.Add(t); err != nil {
			return err
		}
	}
	if q.HasCycle() {
		return fmt.Errorf("orchestration: plan has a dependency cycle")
	}
	return nil
}

// extractJSONObject returns the first {...} object in s.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return ""
}
