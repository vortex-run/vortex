package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/vortex-run/vortex/internal/a2a"
	"github.com/vortex-run/vortex/internal/agents/specialists"
)

// TeamNotifier sends out-of-band notifications (Telegram et al). Defined here
// as an interface so the agents package need not import messaging (which
// imports agents — that would cycle); messaging.Router satisfies it via a thin
// adapter in start.go.
type TeamNotifier interface {
	Notify(title, body string)
}

// TeamConfig configures an AgentTeam.
type TeamConfig struct {
	Client   *a2a.AgentClient
	Server   *a2a.AgentServer
	Notifier TeamNotifier
	WorkDir  string
	// BaseURL is the A2A server base (e.g. http://localhost:9090); used to set
	// each agent card's Endpoint. Optional.
	BaseURL string
}

// AgentTeam manages the specialist agent pipeline (coder → tester → reviewer)
// over A2A, with checkpoints between steps for retries.
type AgentTeam struct {
	config TeamConfig
	code   *specialists.CodeAgent
	test   *specialists.TestAgent
	review *specialists.ReviewAgent
}

// agentIDs maps a role to the registered agent id.
const (
	codeAgentID   = "code-agent"
	testAgentID   = "test-agent"
	reviewAgentID = "review-agent"
)

// NewAgentTeam creates the specialist agents, wiring each to the shared
// gateway + a trusted tool executor + the A2A client, and registers them with
// the A2A server.
func NewAgentTeam(cfg TeamConfig, gateway AIGateway, tools *ToolRegistry) *AgentTeam {
	runTool := toolFunc(tools)
	model := ""

	mkCard := func(id, name, role string) a2a.AgentCard {
		return a2a.AgentCard{ID: id, Name: name, Role: role, Version: "1.0.0",
			AIModel: model, Endpoint: strings.TrimRight(cfg.BaseURL, "/") + "/a2a/agents/" + id,
			Status: a2a.StatusIdle}
	}

	code := specialists.NewCodeAgent(specialists.NewBaseAgent(
		mkCard(codeAgentID, "VORTEX Code Agent", "coder"), gateway, runTool, cfg.Client, cfg.WorkDir))
	test := specialists.NewTestAgent(specialists.NewBaseAgent(
		mkCard(testAgentID, "VORTEX Test Agent", "tester"), gateway, runTool, cfg.Client, cfg.WorkDir))
	review := specialists.NewReviewAgent(specialists.NewBaseAgent(
		mkCard(reviewAgentID, "VORTEX Review Agent", "reviewer"), gateway, runTool, cfg.Client, cfg.WorkDir))

	t := &AgentTeam{config: cfg, code: code, test: test, review: review}
	if cfg.Server != nil {
		cfg.Server.Register(code)
		cfg.Server.Register(test)
		cfg.Server.Register(review)
	}
	return t
}

// toolFunc adapts a *ToolRegistry into the specialists.ToolFunc closure.
func toolFunc(tools *ToolRegistry) specialists.ToolFunc {
	if tools == nil {
		return nil
	}
	return func(ctx context.Context, name string, params map[string]any) (any, error) {
		tool, err := tools.Get(name)
		if err != nil {
			return nil, err
		}
		return tool.Execute(ctx, params)
	}
}

// Cards returns the team's agent cards (for status surfaces).
func (t *AgentTeam) Cards() []a2a.AgentCard {
	return []a2a.AgentCard{t.code.Card(), t.test.Card(), t.review.Card()}
}

// TeamStep is one planned step of the pipeline.
type TeamStep struct {
	AgentRole string          `json:"agent_role"`
	Goal      string          `json:"goal"`
	DependsOn []string        `json:"depends_on"`
	Status    string          `json:"status"`
	Result    *a2a.TaskResult `json:"result,omitempty"`
}

// TeamPlan is the ordered set of steps for a goal.
type TeamPlan struct {
	Goal      string     `json:"goal"`
	SessionID string     `json:"session_id"`
	Steps     []TeamStep `json:"steps"`
}

// planSystemPrompt asks the AI to plan using the available specialist roles.
const planSystemPrompt = `Create a step-by-step plan using these specialist agents: coder, tester, reviewer.
Return ONLY JSON, no prose:
{"steps":[{"agent_role":"coder","goal":"...","depends_on":[]}]}`

// Plan asks the AI for a pipeline, falling back to the default coder→tester→
// reviewer plan when the AI is unavailable or returns nothing usable.
func (t *AgentTeam) Plan(ctx context.Context, goal, sessionID string, gateway AIGateway) (*TeamPlan, error) {
	plan := &TeamPlan{Goal: goal, SessionID: sessionID}
	if gateway != nil {
		reply, err := gateway.Complete(ctx, goal, planSystemPrompt)
		if err == nil {
			var parsed struct {
				Steps []TeamStep `json:"steps"`
			}
			if json.Unmarshal([]byte(stripJSONFences(reply)), &parsed) == nil && len(parsed.Steps) > 0 {
				plan.Steps = parsed.Steps
				return plan, nil
			}
		}
	}
	plan.Steps = defaultSteps(goal)
	return plan, nil
}

// defaultSteps is the standard coder → tester → reviewer pipeline.
func defaultSteps(goal string) []TeamStep {
	return []TeamStep{
		{AgentRole: "coder", Goal: "Implement: " + goal},
		{AgentRole: "tester", Goal: "Run and verify tests for the implementation", DependsOn: []string{"coder"}},
		{AgentRole: "reviewer", Goal: "Review the final code for quality", DependsOn: []string{"tester"}},
	}
}

// TeamResult is the outcome of executing a plan.
type TeamResult struct {
	Success     bool          `json:"success"`
	Goal        string        `json:"goal"`
	Steps       []TeamStep    `json:"steps"`
	Files       []string      `json:"files"`
	TestsPassed int           `json:"tests_passed"`
	ReviewScore int           `json:"review_score"`
	Duration    time.Duration `json:"duration"`
	CostUSD     float64       `json:"cost_usd"`
	FailedAt    string        `json:"failed_at"`
	Error       string        `json:"error"`
}

// maxStepRetries is how many times a failed step is retried (sending failures
// back to the coder).
const maxStepRetries = 2

// Execute runs the plan's steps sequentially, passing each result forward as
// context. A failing tester/reviewer step sends the issues back to the coder
// and retries (up to maxStepRetries).
func (t *AgentTeam) Execute(ctx context.Context, plan *TeamPlan, progressFn func(string)) (*TeamResult, error) {
	start := time.Now()
	res := &TeamResult{Goal: plan.Goal, Steps: plan.Steps}
	progress := func(s string) {
		if progressFn != nil {
			progressFn(s)
		}
	}

	fileSet := map[string]bool{}
	prevContext := ""

	for i := range plan.Steps {
		step := &plan.Steps[i]
		agentID, agent := t.agentFor(step.AgentRole)
		if agent == nil {
			step.Status = "skipped"
			continue
		}

		var stepResult *a2a.TaskResult
		var lastErr string
		for attempt := 0; attempt <= maxStepRetries; attempt++ {
			progress(fmt.Sprintf("[%s] Starting...", step.AgentRole))
			task := a2a.NewTask("coordinator", agentID, step.Goal, plan.SessionID)
			task.Context = prevContext
			task.Files = mapKeys(fileSet)

			result := agent.HandleTask(ctx, *task, func(p a2a.Progress) {
				if p.Message != "" {
					progress(fmt.Sprintf("[%s] %s", step.AgentRole, p.Message))
				}
			})
			stepResult = &result

			if result.Success {
				progress(fmt.Sprintf("[%s] ✓ %s", step.AgentRole, summaryLine(result.Output)))
				break
			}
			lastErr = strings.Join(result.Errors, "; ")
			progress(fmt.Sprintf("[%s] ✗ %s", step.AgentRole, summaryLine(lastErr)))

			// On a tester/reviewer failure, send the issues back to the coder
			// and retry this step after the coder revises.
			if attempt < maxStepRetries && (step.AgentRole == "tester" || step.AgentRole == "reviewer") {
				progress("[coder] Revising based on feedback...")
				fix := a2a.NewTask("coordinator", codeAgentID,
					"Fix these issues:\n"+lastErr, plan.SessionID)
				fix.Context = prevContext
				fix.Files = mapKeys(fileSet)
				fixResult := t.code.HandleTask(ctx, *fix, func(p a2a.Progress) {
					if p.Message != "" {
						progress("[coder] " + p.Message)
					}
				})
				for _, f := range fixResult.Files {
					fileSet[f] = true
				}
			}
		}

		step.Result = stepResult
		if stepResult == nil || !stepResult.Success {
			res.Success = false
			res.FailedAt = step.AgentRole
			res.Error = lastErr
			res.Duration = time.Since(start)
			res.Files = mapKeys(fileSet)
			t.notify("✗ VORTEX team task failed", res.Summary())
			return res, nil
		}
		step.Status = "done"

		// Accumulate outputs for the next step's context.
		for _, f := range stepResult.Files {
			fileSet[f] = true
		}
		if step.AgentRole == "reviewer" {
			res.ReviewScore = stepResult.Score
		}
		if step.AgentRole == "tester" {
			res.TestsPassed = countPassed(stepResult.Output)
		}
		prevContext += fmt.Sprintf("\n[%s result]\n%s\n", step.AgentRole, stepResult.Output)
	}

	res.Success = true
	res.Files = mapKeys(fileSet)
	res.Duration = time.Since(start)
	t.notify("✓ VORTEX team task complete", res.Summary())
	return res, nil
}

// agentFor returns the registered id + agent for a role.
func (t *AgentTeam) agentFor(role string) (string, a2a.Agent) {
	switch role {
	case "coder":
		return codeAgentID, t.code
	case "tester":
		return testAgentID, t.test
	case "reviewer":
		return reviewAgentID, t.review
	default:
		return "", nil
	}
}

// notify sends a team notification when a notifier is configured.
func (t *AgentTeam) notify(title, body string) {
	if t.config.Notifier != nil {
		t.config.Notifier.Notify(title, body)
	}
}

// Summary renders the team result for the user / Telegram.
func (r *TeamResult) Summary() string {
	if !r.Success {
		var b strings.Builder
		fmt.Fprintf(&b, "✗ Task failed at %s stage\n", r.FailedAt)
		if r.Error != "" {
			b.WriteString("Error: " + r.Error + "\n")
		}
		if len(r.Files) > 0 {
			b.WriteString("Files created: " + strings.Join(r.Files, ", "))
		}
		return strings.TrimRight(b.String(), "\n")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "✓ Task complete in %s\n", r.Duration.Round(time.Second))
	if len(r.Files) > 0 {
		b.WriteString("📝 Files: " + strings.Join(r.Files, ", ") + "\n")
	}
	if r.TestsPassed > 0 {
		fmt.Fprintf(&b, "✓ Tests: %d passing\n", r.TestsPassed)
	}
	if r.ReviewScore > 0 {
		approved := "approved"
		if r.ReviewScore < reviewApprovalScore {
			approved = "needs fixes"
		}
		fmt.Fprintf(&b, "⭐ Review: %d/10 (%s)\n", r.ReviewScore, approved)
	}
	fmt.Fprintf(&b, "💰 Cost: $%.3f", r.CostUSD)
	return b.String()
}

// reviewApprovalScore mirrors the reviewer's approval threshold.
const reviewApprovalScore = 7

// --- small helpers ----------------------------------------------------------

// stripJSONFences removes a leading/trailing ```lang fence.
func stripJSONFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimRight(s, "\n"), "```"))
}

// mapKeys returns the keys of a set as a slice.
func mapKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// summaryLine returns the first non-empty line of s (for compact progress).
func summaryLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// countPassed extracts the passing-test count from a tester summary line like
// "✓ 12/12 tests pass".
func countPassed(output string) int {
	for _, l := range strings.Split(output, "\n") {
		if strings.Contains(l, "tests pass") {
			var p, total int
			if _, err := fmt.Sscanf(strings.TrimLeft(l, "✓ "), "%d/%d tests pass", &p, &total); err == nil {
				return p
			}
		}
	}
	return 0
}
