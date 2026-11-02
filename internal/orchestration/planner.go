package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vortex-run/vortex/internal/a2a"
	"github.com/vortex-run/vortex/internal/agents"
)

// plannerSystemPrompt instructs the model to decompose a goal into a task DAG.
const plannerSystemPrompt = `You are a planner that decomposes a goal into a small set of tasks for specialized agents. Available agent types: research, devops, data_pipeline, build_app, general. Alternatively set required_capabilities (and optional permission_profile) for capability-based routing. Respond with ONLY this JSON:
{"tasks":[{"id":"t1","name":"...","agent":"research","input":"...","depends_on":[],"max_attempts":1,"verification_criteria":"nonempty"},{"id":"t2","name":"...","required_capabilities":["research.web"],"input":"...","depends_on":["t1"],"verification_criteria":"contains:summary"}]}
Rules: ids are unique short strings; depends_on lists ids that must finish first; keep it to 2-6 tasks; no cycles; max_attempts is optional 1-3 (default 1); verification_criteria is optional and must be one of: omit or empty (skip), "nonempty", "contains:<substr>", "prefix:<substr>" — no free text; use either agent or required_capabilities per task, not both. Return only JSON.`

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
	catalog *a2a.Catalog
}

// NewPlanner constructs a planner over an AI gateway.
func NewPlanner(gateway agents.AIGateway) *Planner {
	return &Planner{gateway: gateway}
}

// SetCatalog overrides the agent registry used for required_capabilities
// resolution (defaults to a2a.DefaultCatalog()).
func (p *Planner) SetCatalog(c *a2a.Catalog) { p.catalog = c }

func (p *Planner) catalogOrDefault() *a2a.Catalog {
	if p.catalog != nil {
		return p.catalog
	}
	return a2a.DefaultCatalog()
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
	if err := p.validatePlan(tasks); err != nil {
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
			ID                   string   `json:"id"`
			Name                 string   `json:"name"`
			Agent                string   `json:"agent"`
			RequiredCapabilities []string `json:"required_capabilities"`
			PermissionProfile    string   `json:"permission_profile"`
			Input                string   `json:"input"`
			DependsOn            []string `json:"depends_on"`
			MaxAttempts          int      `json:"max_attempts"`
			VerificationCriteria string   `json:"verification_criteria"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(js), &raw); err != nil {
		return nil
	}
	tasks := make([]*Task, 0, len(raw.Tasks))
	for _, r := range raw.Tasks {
		t := &Task{
			ID: r.ID, Name: r.Name, AgentType: r.Agent,
			Input: r.Input, DependsOn: r.DependsOn,
			MaxAttempts:          r.MaxAttempts,
			VerificationCriteria: r.VerificationCriteria,
		}
		if len(r.RequiredCapabilities) > 0 || strings.TrimSpace(r.PermissionProfile) != "" {
			setTaskCapabilityMeta(t, r.RequiredCapabilities, r.PermissionProfile)
		}
		tasks = append(tasks, t)
	}
	return tasks
}

const (
	metaRequiredCapabilities = "required_capabilities"
	metaPermissionProfile    = "permission_profile"
)

func setTaskCapabilityMeta(t *Task, caps []string, profile string) {
	if t.Meta == nil {
		t.Meta = map[string]any{}
	}
	if len(caps) > 0 {
		t.Meta[metaRequiredCapabilities] = caps
	}
	if p := strings.TrimSpace(profile); p != "" {
		t.Meta[metaPermissionProfile] = p
	}
}

func taskCapabilityQuery(t *Task) (caps []string, profile string, ok bool) {
	if t == nil || t.Meta == nil {
		return nil, "", false
	}
	raw, _ := t.Meta[metaRequiredCapabilities].([]string)
	if raw == nil {
		if anySlice, ok2 := t.Meta[metaRequiredCapabilities].([]any); ok2 {
			for _, v := range anySlice {
				if s, ok3 := v.(string); ok3 && strings.TrimSpace(s) != "" {
					raw = append(raw, s)
				}
			}
		}
	}
	if p, _ := t.Meta[metaPermissionProfile].(string); p != "" {
		profile = p
	}
	if len(raw) == 0 && profile == "" {
		return nil, "", false
	}
	return raw, profile, true
}

// validatePlan checks IDs, agent types, dependency references, and acyclicity.
func validatePlan(tasks []*Task) error {
	return (&Planner{}).validatePlan(tasks)
}

func (p *Planner) validatePlan(tasks []*Task) error {
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
		caps, profile, useCaps := taskCapabilityQuery(t)
		if useCaps {
			if t.AgentType != "" {
				return fmt.Errorf("orchestration: task %q sets both agent and required_capabilities", t.ID)
			}
			_, route, err := p.catalogOrDefault().ResolveOrchestration(a2a.SelectQuery{
				RequiredCapabilities: caps,
				PermissionProfile:    profile,
			})
			if err != nil {
				return fmt.Errorf("orchestration: task %q: %w", t.ID, err)
			}
			t.AgentType = route
		} else if t.AgentType != "" && !validAgentTypes[t.AgentType] {
			return fmt.Errorf("orchestration: task %q has unknown agent type %q", t.ID, t.AgentType)
		}
		if t.MaxAttempts < 0 || t.MaxAttempts > maxTaskAttempts {
			return fmt.Errorf("orchestration: task %q max_attempts %d out of range 0..%d", t.ID, t.MaxAttempts, maxTaskAttempts)
		}
		if err := validateVerificationCriteria(t.VerificationCriteria); err != nil {
			return fmt.Errorf("orchestration: task %q: %w", t.ID, err)
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

// validateVerificationCriteria rejects free-text criteria before runtime execution.
func validateVerificationCriteria(criteria string) error {
	criteria = strings.TrimSpace(criteria)
	if criteria == "" || criteria == "nonempty" {
		return nil
	}
	if strings.HasPrefix(criteria, "contains:") {
		if strings.TrimPrefix(criteria, "contains:") == "" {
			return fmt.Errorf("unsupported verification_criteria: empty contains substring")
		}
		return nil
	}
	if strings.HasPrefix(criteria, "prefix:") {
		if strings.TrimPrefix(criteria, "prefix:") == "" {
			return fmt.Errorf("unsupported verification_criteria: empty prefix")
		}
		return nil
	}
	return fmt.Errorf("unsupported verification_criteria %q (use nonempty, contains:<substr>, or prefix:<substr>)", criteria)
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
