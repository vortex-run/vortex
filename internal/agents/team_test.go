package agents

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/vortex-run/vortex/internal/a2a"
)

// scriptedGateway returns a reply chosen by which specialist system prompt it
// sees, so one gateway can drive the whole pipeline.
type scriptedGateway struct {
	mu        sync.Mutex
	codeReply string
	reviewRpl string
	planReply string
}

func (g *scriptedGateway) Complete(_ context.Context, _, system string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	low := strings.ToLower(system)
	switch {
	case strings.Contains(low, "task planner"):
		return g.planReply, nil
	case strings.Contains(low, "code reviewer"):
		return g.reviewRpl, nil
	case strings.Contains(low, "software engineer"):
		return g.codeReply, nil
	default:
		return "{}", nil
	}
}

// memTool is an in-memory tool that records writes and serves canned outputs.
type memTool struct {
	name    string
	written map[string]string
	output  string
}

func (t *memTool) Name() string        { return t.name }
func (t *memTool) Description() string { return t.name }
func (t *memTool) Execute(_ context.Context, params map[string]any) (any, error) {
	if t.name == "write_file" {
		p, _ := params["path"].(string)
		c, _ := params["content"].(string)
		t.written[p] = c
		return map[string]any{"path": p}, nil
	}
	return t.output, nil
}

// teamRegistry builds a registry with the tools the specialists use, sharing a
// write map and a configurable terminal output.
func teamRegistry(t *testing.T, written map[string]string, termOut string) *ToolRegistry {
	t.Helper()
	reg := NewToolRegistry()
	for _, name := range []string{"read_file", "list_directory"} {
		_ = reg.Register(&memTool{name: name, written: written, output: ""})
	}
	_ = reg.Register(&memTool{name: "write_file", written: written})
	_ = reg.Register(&memTool{name: "run_terminal", written: written, output: termOut})
	return reg
}

func newTestTeam(t *testing.T, gw AIGateway, written map[string]string, termOut string) *AgentTeam {
	t.Helper()
	return NewAgentTeam(TeamConfig{WorkDir: t.TempDir()}, gw, teamRegistry(t, written, termOut))
}

const codePlanJSON = `{"files":[{"path":"hello.py","content":"print('hi')","is_new":true}],"summary":"hello"}`
const goodReviewJSON = `{"score":9,"approved":true,"summary":"clean"}`
const badReviewJSON = `{"score":4,"approved":false,"issues":[{"severity":"high","file":"hello.py","line":1,"message":"bad"}],"summary":"fix it"}`

func TestTeam_RegistersAgents(t *testing.T) {
	server := a2a.NewAgentServer()
	NewAgentTeam(TeamConfig{Server: server, WorkDir: t.TempDir()}, &scriptedGateway{}, teamRegistry(t, map[string]string{}, ""))
	cards := server.List()
	if len(cards) != 3 {
		t.Fatalf("registered %d agents, want 3", len(cards))
	}
	roles := map[string]bool{}
	for _, c := range cards {
		roles[c.Role] = true
	}
	for _, want := range []string{"coder", "tester", "reviewer"} {
		if !roles[want] {
			t.Errorf("missing %s agent", want)
		}
	}
}

func TestTeam_DefaultPlan(t *testing.T) {
	team := newTestTeam(t, &scriptedGateway{}, map[string]string{}, "")
	plan, err := team.Plan(context.Background(), "build x", "s1", nil) // nil gateway → default
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 3 {
		t.Fatalf("default plan = %d steps, want 3", len(plan.Steps))
	}
	if plan.Steps[0].AgentRole != "coder" || plan.Steps[1].AgentRole != "tester" || plan.Steps[2].AgentRole != "reviewer" {
		t.Errorf("default plan roles wrong: %+v", plan.Steps)
	}
}

func TestTeam_AIPlan(t *testing.T) {
	// A non-coding goal accepts the AI plan verbatim (even a single step).
	gw := &scriptedGateway{planReply: `{"steps":[{"agent_role":"coder","goal":"do it"}]}`}
	team := newTestTeam(t, gw, map[string]string{}, "")
	plan, err := team.Plan(context.Background(), "explain x", "s1", gw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Goal != "do it" {
		t.Errorf("AI plan = %+v", plan.Steps)
	}
}

func TestTeam_CodingPlanAlwaysThreeSteps(t *testing.T) {
	// A coding goal must always yield the full coder→tester→reviewer pipeline so
	// checkpoints fire between steps — even when the AI collapses to one step.
	gw := &scriptedGateway{planReply: `{"steps":[{"agent_role":"coder","goal":"just code it"}]}`}
	team := newTestTeam(t, gw, map[string]string{}, "")
	plan, err := team.Plan(context.Background(), "build a flask calculator", "s1", gw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 3 {
		t.Fatalf("coding plan = %d steps, want 3: %+v", len(plan.Steps), plan.Steps)
	}
	roles := []string{plan.Steps[0].AgentRole, plan.Steps[1].AgentRole, plan.Steps[2].AgentRole}
	if roles[0] != "coder" || roles[1] != "tester" || roles[2] != "reviewer" {
		t.Errorf("roles = %v, want [coder tester reviewer]", roles)
	}
}

func TestTeam_CodingPlanFallbackNoGateway(t *testing.T) {
	team := newTestTeam(t, &scriptedGateway{}, map[string]string{}, "")
	plan, _ := team.Plan(context.Background(), "create a web scraper", "s1", nil)
	if len(plan.Steps) != 3 {
		t.Errorf("coding fallback = %d steps, want 3", len(plan.Steps))
	}
}

func TestIsCodingGoal(t *testing.T) {
	for _, g := range []string{"build a server", "create x", "fix the bug", "implement auth", "refactor main.go"} {
		if !isCodingGoal(g) {
			t.Errorf("isCodingGoal(%q) = false, want true", g)
		}
	}
	for _, g := range []string{"what is 2+2", "explain this code", "how does http work"} {
		if isCodingGoal(g) {
			t.Errorf("isCodingGoal(%q) = true, want false", g)
		}
	}
}

func TestTeam_ExecuteHappyPath(t *testing.T) {
	written := map[string]string{}
	gw := &scriptedGateway{codeReply: codePlanJSON, reviewRpl: goodReviewJSON}
	team := newTestTeam(t, gw, written, "--- PASS: TestX (0.00s)\nok")

	plan := &TeamPlan{Goal: "build hello", SessionID: "s1", Steps: defaultSteps("build hello")}
	var updates []string
	res, err := team.Execute(context.Background(), plan, func(s string) { updates = append(updates, s) })
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}
	if written["hello.py"] != "print('hi')" {
		t.Errorf("code agent did not write the file: %v", written)
	}
	if res.ReviewScore != 9 {
		t.Errorf("review score = %d, want 9", res.ReviewScore)
	}
	// Steps ran in order.
	joined := strings.Join(updates, "\n")
	if !strings.Contains(joined, "[coder]") || !strings.Contains(joined, "[tester]") || !strings.Contains(joined, "[reviewer]") {
		t.Errorf("not all agents ran:\n%s", joined)
	}
}

func TestTeam_PublishesToBus(t *testing.T) {
	written := map[string]string{}
	gw := &scriptedGateway{codeReply: codePlanJSON, reviewRpl: goodReviewJSON}
	bus := a2a.NewMessageBus()
	team := NewAgentTeam(
		TeamConfig{WorkDir: t.TempDir(), Bus: bus},
		gw, teamRegistry(t, written, "--- PASS: TestX (0.00s)\nok"),
	)

	plan := &TeamPlan{Goal: "build hello", SessionID: "s1", Steps: defaultSteps("build hello")}
	if _, err := team.Execute(context.Background(), plan, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	msgs := bus.History("s1", 0)
	if len(msgs) == 0 {
		t.Fatal("bus received no messages during Execute — comms panel would be empty")
	}
	var sawTask, sawResult, sawProgress bool
	for _, m := range msgs {
		switch m.Type {
		case a2a.MsgTask:
			sawTask = true
		case a2a.MsgResult:
			sawResult = true
		case a2a.MsgProgress:
			sawProgress = true
		}
	}
	if !sawTask || !sawResult || !sawProgress {
		t.Errorf("missing message types: task=%v result=%v progress=%v", sawTask, sawResult, sawProgress)
	}
}

func TestTeam_PublishesToolResults(t *testing.T) {
	written := map[string]string{}
	gw := &scriptedGateway{codeReply: codePlanJSON, reviewRpl: goodReviewJSON}
	bus := a2a.NewMessageBus()
	team := NewAgentTeam(
		TeamConfig{WorkDir: t.TempDir(), Bus: bus},
		gw, teamRegistry(t, written, "--- PASS: TestX (0.00s)\nok"),
	)
	plan := &TeamPlan{Goal: "build hello", SessionID: "s1", Steps: defaultSteps("build hello")}
	if _, err := team.Execute(context.Background(), plan, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// The coder's write_file should have produced a tool_result bus message
	// encoded as "tool|target|content".
	var toolMsgs []a2a.BusMessage
	for _, m := range bus.History("", 0) {
		if m.Type == a2a.MsgToolResult {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) == 0 {
		t.Fatal("expected at least one tool_result bus message from write_file")
	}
	if !strings.HasPrefix(toolMsgs[0].Content, "write_file|") {
		t.Errorf("tool_result content = %q, want write_file|... ", toolMsgs[0].Content)
	}
}

func TestTeam_PublishesPlanBeforeExecution(t *testing.T) {
	gw := &scriptedGateway{codeReply: codePlanJSON, reviewRpl: goodReviewJSON}
	bus := a2a.NewMessageBus()
	team := NewAgentTeam(TeamConfig{WorkDir: t.TempDir(), Bus: bus}, gw, teamRegistry(t, map[string]string{}, "--- PASS: TestX (0.00s)\nok"))
	plan := &TeamPlan{Goal: "build hello", SessionID: "s1", Steps: defaultCodingPlan("build hello")}
	if _, err := team.Execute(context.Background(), plan, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	msgs := bus.History("s1", 0)
	if len(msgs) == 0 || msgs[0].Type != a2a.MsgPlan {
		t.Fatalf("first bus message should be the plan, got %+v", msgs)
	}
	if !strings.Contains(msgs[0].Content, "Here's my plan") || !strings.Contains(msgs[0].Content, "Code Agent") {
		t.Errorf("plan content = %q", msgs[0].Content)
	}
}

func TestToolResultLine(t *testing.T) {
	if got := toolResultLine("write_file", map[string]any{"path": "calc.py", "content": "x\ny"}, nil); got != "write_file|calc.py|x\ny" {
		t.Errorf("write_file line = %q", got)
	}
	if got := toolResultLine("run_terminal", map[string]any{"command": "pytest"}, "exit 0"); got != "run_terminal|pytest|exit 0" {
		t.Errorf("run_terminal line = %q", got)
	}
	if got := toolResultLine("read_file", map[string]any{"path": "x"}, "data"); got != "" {
		t.Errorf("read_file should not produce a tool card, got %q", got)
	}
}

func TestTeam_PassesContextForward(t *testing.T) {
	written := map[string]string{}
	gw := &scriptedGateway{codeReply: codePlanJSON, reviewRpl: goodReviewJSON}
	team := newTestTeam(t, gw, written, "--- PASS: TestX (0.00s)")
	plan := &TeamPlan{Goal: "g", SessionID: "s1", Steps: defaultSteps("g")}
	res, _ := team.Execute(context.Background(), plan, nil)
	// The file written by the coder is carried into the result.
	if len(res.Files) == 0 {
		t.Error("files produced by the coder should appear in the result")
	}
}

func TestTeam_RetriesOnReviewFailure(t *testing.T) {
	written := map[string]string{}
	// Reviewer rejects → coder revises → step retried (still rejected here).
	gw := &scriptedGateway{codeReply: codePlanJSON, reviewRpl: badReviewJSON}
	team := newTestTeam(t, gw, written, "--- PASS: TestX (0.00s)")
	plan := &TeamPlan{Goal: "g", SessionID: "s1", Steps: defaultSteps("g")}

	var updates []string
	res, _ := team.Execute(context.Background(), plan, func(s string) { updates = append(updates, s) })
	if res.Success {
		t.Error("a persistently-rejected review should fail the team task")
	}
	if res.FailedAt != "reviewer" {
		t.Errorf("FailedAt = %q, want reviewer", res.FailedAt)
	}
	// The coder was asked to revise at least once.
	if !strings.Contains(strings.Join(updates, "\n"), "Revising") {
		t.Errorf("expected a coder revision on review failure:\n%s", strings.Join(updates, "\n"))
	}
}

func TestTeamResult_Summary(t *testing.T) {
	ok := (&TeamResult{Success: true, Files: []string{"main.py"}, TestsPassed: 12, ReviewScore: 9}).Summary()
	for _, want := range []string{"✓ Task complete", "main.py", "12 passing", "9/10 (approved)"} {
		if !strings.Contains(ok, want) {
			t.Errorf("success summary missing %q:\n%s", want, ok)
		}
	}
	fail := (&TeamResult{Success: false, FailedAt: "tester", Error: "3 tests failing", Files: []string{"main.py"}}).Summary()
	for _, want := range []string{"✗ Task failed at tester", "3 tests failing", "main.py"} {
		if !strings.Contains(fail, want) {
			t.Errorf("fail summary missing %q:\n%s", want, fail)
		}
	}
}
