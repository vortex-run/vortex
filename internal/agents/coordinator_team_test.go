package agents

import (
	"context"
	"strings"
	"testing"
)

func TestShouldUseTeam(t *testing.T) {
	yes := []string{
		"build a FastAPI auth system",
		"create a python web scraper",
		"implement JWT login",
		"refactor main.py to use async",
		"fix the bug in auth.py",
		"write a calculator",
	}
	for _, m := range yes {
		if !shouldUseTeam(m) {
			t.Errorf("shouldUseTeam(%q) = false, want true", m)
		}
	}
	no := []string{
		"what is the capital of France?",
		"how does this work?",
		"/ls",
		"/build something", // slash command → not the team
		"explain the codebase",
	}
	for _, m := range no {
		if shouldUseTeam(m) {
			t.Errorf("shouldUseTeam(%q) = true, want false", m)
		}
	}
}

// teamCoordinator builds a coordinator with team mode enabled over a scripted
// gateway + in-memory tools.
func teamCoordinator(t *testing.T, gw AIGateway, written map[string]string, termOut string) *Coordinator {
	t.Helper()
	c := newTestCoordinator(t, gw)
	team := NewAgentTeam(TeamConfig{WorkDir: t.TempDir()}, gw, teamRegistry(t, written, termOut))
	c.SetTeam(team)
	return c
}

func TestCoordinator_TeamModeRunsPipeline(t *testing.T) {
	written := map[string]string{}
	gw := &scriptedGateway{
		codeReply: codePlanJSON,
		reviewRpl: goodReviewJSON,
		planReply: `{"steps":[{"agent_role":"coder","goal":"implement"},{"agent_role":"reviewer","goal":"review"}]}`,
	}
	c := teamCoordinator(t, gw, written, "--- PASS: TestX (0.00s)")

	out, err := c.HandleMessage(context.Background(), "build a hello world script", "s1")
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !strings.Contains(out, "Task complete") {
		t.Errorf("team summary expected, got:\n%s", out)
	}
	if written["hello.py"] == "" {
		t.Error("team mode should have run the code agent")
	}
}

func TestCoordinator_TeamPrefixForcesPipeline(t *testing.T) {
	written := map[string]string{}
	gw := &scriptedGateway{
		codeReply: codePlanJSON,
		reviewRpl: goodReviewJSON,
		planReply: `{"steps":[{"agent_role":"coder","goal":"implement"},{"agent_role":"reviewer","goal":"review"}]}`,
	}
	c := teamCoordinator(t, gw, written, "--- PASS: TestX (0.00s)")

	// "a flask calculator" does NOT match shouldUseTeam keywords, but the /team
	// prefix (sent by `vortex code --team`) must force the pipeline anyway.
	if shouldUseTeam("a flask calculator") {
		t.Fatal("precondition: goal should not match team keywords")
	}
	out, err := c.HandleMessage(context.Background(), "/team a flask calculator", "s1")
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if written["hello.py"] == "" {
		t.Error("/team must run the code agent and write files")
	}
	// The reply must be the clean team summary, not orchestration internals.
	if !strings.Contains(out, "Task complete") {
		t.Errorf("expected clean team summary, got:\n%s", out)
	}
	for _, banned := range []string{"proven skill", "(tool:", "tasks:", "Goal:"} {
		if strings.Contains(out, banned) {
			t.Errorf("team reply leaked internal data %q:\n%s", banned, out)
		}
	}
}

func TestCoordinator_TeamPrefixFallsBackWhenNoTeam(t *testing.T) {
	// /team with no team wired must not error — it strips the prefix and proceeds.
	c := newTestCoordinator(t, &scriptedGateway{codeReply: codePlanJSON})
	if _, err := c.HandleMessage(context.Background(), "/team build a thing", "s1"); err != nil {
		t.Fatalf("/team without a team should not error: %v", err)
	}
}

func TestCoordinator_TeamModeSkipsNonCoding(t *testing.T) {
	written := map[string]string{}
	gw := &scriptedGateway{codeReply: codePlanJSON, reviewRpl: goodReviewJSON}
	// Also wire a stub answer gateway behaviour: the StubAIGateway path answers
	// general questions. Here the scripted gateway returns "{}" for the intent
	// classification + answer, which is fine — we only assert the team did NOT
	// run (no file written).
	c := teamCoordinator(t, gw, written, "")

	_, _ = c.HandleMessage(context.Background(), "what is 2 plus 2?", "s1")
	if len(written) != 0 {
		t.Errorf("a question should not trigger the team: %v", written)
	}
}

func TestCoordinator_SingleAgentWhenTeamDisabled(t *testing.T) {
	// No SetTeam → team mode off → a "build" message does NOT run the pipeline;
	// it falls through to the normal single-agent path.
	c := newTestCoordinator(t, &scriptedGateway{codeReply: codePlanJSON})
	if _, err := c.HandleMessage(context.Background(), "build a thing", "s1"); err != nil {
		t.Fatalf("HandleMessage without team: %v", err)
	}
	team, on := c.teamHandler()
	if on || team != nil {
		t.Error("team mode should be off when SetTeam was never called")
	}
}

func TestFormatPlan(t *testing.T) {
	plan := &TeamPlan{Steps: defaultSteps("build x")}
	out := formatPlan(plan)
	for _, want := range []string{"Code Agent", "Test Agent", "Review Agent", "Starting now"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatPlan missing %q:\n%s", want, out)
		}
	}
}

func TestSetTeamToggles(t *testing.T) {
	c := newTestCoordinator(t, &scriptedGateway{})
	if _, on := c.teamHandler(); on {
		t.Error("team mode should start off")
	}
	team := NewAgentTeam(TeamConfig{WorkDir: t.TempDir()}, &scriptedGateway{}, teamRegistry(t, map[string]string{}, ""))
	c.SetTeam(team)
	if _, on := c.teamHandler(); !on {
		t.Error("SetTeam should enable team mode")
	}
	c.SetTeam(nil)
	if _, on := c.teamHandler(); on {
		t.Error("SetTeam(nil) should disable team mode")
	}
}
