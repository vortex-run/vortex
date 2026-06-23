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
	// Bus, when set, publishes inter-agent communication for the UI to render.
	Bus *a2a.MessageBus
	// Checkpoints, when set, pauses between agent steps for human review
	// (approve/edit/reject) before passing work to the next agent.
	Checkpoints *a2a.CheckpointManager
}

// AgentTeam manages the specialist agent pipeline (coder → tester → reviewer)
// over A2A, with checkpoints between steps for retries.
type AgentTeam struct {
	config  TeamConfig
	code    *specialists.CodeAgent
	test    *specialists.TestAgent
	review  *specialists.ReviewAgent
	runTool specialists.ToolFunc // for reading previews + applying edits
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
	runTool := toolFuncWithBus(tools, cfg.Bus)
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

	t := &AgentTeam{config: cfg, code: code, test: test, review: review, runTool: runTool}
	if cfg.Server != nil {
		cfg.Server.Register(code)
		cfg.Server.Register(test)
		cfg.Server.Register(review)
	}
	return t
}

// toolFuncWithBus adapts a *ToolRegistry into the specialists.ToolFunc closure,
// additionally publishing a "tool_result" bus message after a mutating tool
// (write_file/edit_file/run_terminal) succeeds, so the UI can render collapsible
// tool-use rows like Claude Code.
func toolFuncWithBus(tools *ToolRegistry, bus *a2a.MessageBus) specialists.ToolFunc {
	if tools == nil {
		return nil
	}
	return func(ctx context.Context, name string, params map[string]any) (any, error) {
		tool, err := tools.Get(name)
		if err != nil {
			return nil, err
		}
		res, err := tool.Execute(ctx, params)
		if err == nil && bus != nil {
			if content := toolResultLine(name, params, res); content != "" {
				bus.Publish(a2a.BusMessage{
					From: codeAgentID, To: "user", Type: a2a.MsgToolResult, Content: content,
				})
			}
		}
		return res, err
	}
}

// toolResultLine encodes a mutating tool call as "tool|target|detail" for the
// tool_result bus message (parsed by the TUI into a collapsible row). Returns ""
// for non-mutating tools (reads/lists) that should not appear as tool cards.
func toolResultLine(name string, params map[string]any, res any) string {
	target, _ := params["path"].(string)
	switch name {
	case "write_file", "edit_file":
		content, _ := params["content"].(string)
		if content == "" { // edit_file may carry new content under a different key
			content = specialists.ToolResultString(res)
		}
		return name + "|" + target + "|" + content
	case "run_terminal":
		cmd, _ := params["command"].(string)
		return name + "|" + cmd + "|" + specialists.ToolResultString(res)
	default:
		return ""
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

// planSystemPrompt asks the AI to plan using the available specialist roles. It
// mandates the full coder → tester → reviewer pipeline for any file-touching
// task so checkpoints fire between steps (collapsing to one step would skip
// them).
const planSystemPrompt = `You are a task planner for VORTEX. Break EVERY coding request into exactly these three steps unless the request is trivially simple (e.g. "what is 2+2"):

Step 1: coder — write the implementation
Step 2: tester — run tests and verify it works
Step 3: reviewer — review code quality and security

ALWAYS use all 3 steps for any request that creates or modifies files. NEVER collapse to 1 step for a coding task.

Return ONLY JSON, no prose:
{"steps":[
  {"agent_role":"coder","goal":"...","depends_on":[]},
  {"agent_role":"tester","goal":"...","depends_on":["coder"]},
  {"agent_role":"reviewer","goal":"...","depends_on":["tester"]}
]}`

// codingKeywords mark a goal as a file-touching coding task that must run the
// full 3-step pipeline (so checkpoints fire).
var codingKeywords = []string{
	"build", "create", "write", "make", "implement", "develop", "add", "fix",
	"refactor", "edit", "update", "generate", "scaffold",
}

// isCodingGoal reports whether the goal is a coding/file-touching request.
func isCodingGoal(goal string) bool {
	low := strings.ToLower(goal)
	for _, k := range codingKeywords {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}

// Plan asks the AI for a pipeline. For coding requests it guarantees the full
// coder→tester→reviewer plan (falling back to a hardcoded 3-step plan when the
// AI is unavailable or returns fewer than 2 steps), so checkpoints always fire
// between steps for real builds.
func (t *AgentTeam) Plan(ctx context.Context, goal, sessionID string, gateway AIGateway) (*TeamPlan, error) {
	plan := &TeamPlan{Goal: goal, SessionID: sessionID}
	coding := isCodingGoal(goal)

	if gateway != nil {
		reply, err := gateway.Complete(ctx, goal, planSystemPrompt)
		if err == nil {
			var parsed struct {
				Steps []TeamStep `json:"steps"`
			}
			if json.Unmarshal([]byte(stripJSONFences(reply)), &parsed) == nil && len(parsed.Steps) > 0 {
				// For a coding task, reject a collapsed (<2 step) AI plan — it would
				// skip the checkpoints. Fall through to the hardcoded 3-step plan.
				if !coding || len(parsed.Steps) >= 2 {
					plan.Steps = parsed.Steps
					return plan, nil
				}
			}
		}
	}

	// Hardcoded fallback: always a 3-step plan for coding requests; the generic
	// default otherwise.
	if coding {
		plan.Steps = defaultCodingPlan(goal)
	} else {
		plan.Steps = defaultSteps(goal)
	}
	return plan, nil
}

// defaultCodingPlan returns the guaranteed 3-step coder→tester→reviewer plan for
// a coding goal, used whenever the AI plan is missing or collapsed.
func defaultCodingPlan(goal string) []TeamStep {
	return []TeamStep{
		{AgentRole: "coder", Goal: goal},
		{AgentRole: "tester", Goal: "Test the implementation and verify it works", DependsOn: []string{"coder"}},
		{AgentRole: "reviewer", Goal: "Review code quality and security", DependsOn: []string{"tester"}},
	}
}

// formatPlanSteps renders the plan as a Claude-Code-style "here's my plan"
// block, shown in the chat panel before execution begins.
func formatPlanSteps(plan *TeamPlan) string {
	var b strings.Builder
	b.WriteString("Here's my plan:\n")
	for i, s := range plan.Steps {
		name := s.AgentRole
		switch s.AgentRole {
		case "coder":
			name = "Code Agent"
		case "tester":
			name = "Test Agent"
		case "reviewer":
			name = "Review Agent"
		}
		fmt.Fprintf(&b, "\nStep %d → %s\n  %s", i+1, name, s.Goal)
	}
	b.WriteString("\n\nStarting now...")
	return b.String()
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
	// progress fans a human-readable line to the caller's progressFn AND, when a
	// bus is configured, publishes it as a structured message so the AGENT COMMS
	// panel / SSE feed render the live inter-agent conversation.
	progress := func(s string) {
		if progressFn != nil {
			progressFn(s)
		}
		t.publish(a2a.MsgProgress, "coordinator", "user", s, plan.SessionID)
	}

	// Publish the plan up front so the UI can show what's about to happen before
	// any agent runs (Claude-Code-style "here's my plan").
	t.publish(a2a.MsgPlan, "coordinator", "user", formatPlanSteps(plan), plan.SessionID)

	fileSet := map[string]bool{}
	prevContext := ""

	for i := range plan.Steps {
		step := &plan.Steps[i]
		agentID, agent := t.agentFor(step.AgentRole)
		if agent == nil {
			step.Status = "skipped"
			continue
		}

		// Publish the task hand-off coordinator → specialist for the comms panel.
		t.publish(a2a.MsgTask, "coordinator", agentID, step.Goal, plan.SessionID)

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
				// Publish the specialist → coordinator result for the comms panel.
				t.publish(a2a.MsgResult, agentID, "coordinator", summaryLine(result.Output), plan.SessionID)
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

		// Checkpoint: pause for human review before handing off to the next
		// step (not after the final step). A rejection stops the pipeline; an
		// edit re-applies the user's files and is carried into the next context.
		if t.config.Checkpoints != nil && i < len(plan.Steps)-1 {
			toRole := plan.Steps[i+1].AgentRole
			fromID, _ := t.agentFor(step.AgentRole)
			toID, _ := t.agentFor(toRole)
			progress(fmt.Sprintf("[%s] ⏸ Checkpoint — awaiting your review", step.AgentRole))
			outcome, cperr := t.config.Checkpoints.Create(
				plan.SessionID, fromID, toID, *stepResult, t.filePreviews(stepResult.Files))
			if cperr != nil {
				// Rejected by the user.
				res.Success = false
				res.FailedAt = step.AgentRole + " (rejected by user)"
				res.Error = cperr.Error()
				res.Duration = time.Since(start)
				res.Files = mapKeys(fileSet)
				progress(fmt.Sprintf("[%s] ✗ REJECTED by user", step.AgentRole))
				t.notify("✗ VORTEX team task rejected", res.Summary())
				return res, nil
			}
			switch outcome.Status {
			case a2a.CheckpointEdited:
				for _, e := range outcome.EditedFiles {
					_, _ = t.runToolWrite(ctx, e.Path, e.NewContent)
					fileSet[e.Path] = true
				}
				prevContext += "\n" + a2a.FileEditsAsContext(outcome.EditedFiles)
				progress(fmt.Sprintf("[%s] ✎ EDITED by user — %d file(s)", step.AgentRole, len(outcome.EditedFiles)))
			default:
				progress(fmt.Sprintf("[%s] ✓ APPROVED by user", step.AgentRole))
			}
		}
	}

	res.Success = true
	res.Files = mapKeys(fileSet)
	res.Duration = time.Since(start)
	t.notify("✓ VORTEX team task complete", res.Summary())
	return res, nil
}

// filePreviews reads the modified files into checkpoint previews (best effort).
func (t *AgentTeam) filePreviews(files []string) []a2a.FilePreview {
	var out []a2a.FilePreview
	for _, f := range files {
		content := ""
		if t.runTool != nil {
			if res, err := t.runTool(contextTODO(), "read_file", map[string]any{"path": f}); err == nil {
				content = specialists.ToolResultString(res)
			}
		}
		out = append(out, a2a.FilePreview{
			Path:     f,
			Content:  content,
			Language: languageOf(f),
			Lines:    countLines(content),
			IsNew:    true,
		})
	}
	return out
}

// runToolWrite applies a user edit to a file via the write_file tool.
func (t *AgentTeam) runToolWrite(ctx context.Context, path, content string) (any, error) {
	if t.runTool == nil {
		return nil, fmt.Errorf("agents: no tool executor for checkpoint edits")
	}
	return t.runTool(ctx, "write_file", map[string]any{"path": path, "content": content, "create_dirs": true})
}

// languageOf returns a syntax-hint language for a file path.
func languageOf(path string) string {
	switch {
	case strings.HasSuffix(path, ".go"):
		return "go"
	case strings.HasSuffix(path, ".py"):
		return "python"
	case strings.HasSuffix(path, ".js"):
		return "javascript"
	case strings.HasSuffix(path, ".ts"):
		return "typescript"
	case strings.HasSuffix(path, ".rs"):
		return "rust"
	default:
		return ""
	}
}

// countLines counts newline-separated lines in s.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// contextTODO returns a background context for the best-effort preview read.
func contextTODO() context.Context { return context.Background() }

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

// publish emits a structured message onto the bus (no-op when unconfigured) so
// the AGENT COMMS panel and the /api/agents/comms SSE feed see the live
// inter-agent conversation.
func (t *AgentTeam) publish(msgType, from, to, content, sessionID string) {
	if t.config.Bus == nil || content == "" {
		return
	}
	t.config.Bus.Publish(a2a.BusMessage{
		From:      from,
		To:        to,
		Type:      msgType,
		Content:   content,
		SessionID: sessionID,
	})
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
