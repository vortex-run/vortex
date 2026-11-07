package orchestration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sandeepbollavaram/vortex/internal/a2a"
)

// planGateway returns a fixed plan reply or an error.
type planGateway struct {
	reply string
	err   error
}

func (g planGateway) Complete(context.Context, string, string) (string, error) {
	return g.reply, g.err
}

func TestPlan_DecomposesGoal(t *testing.T) {
	gw := planGateway{reply: `{"tasks":[
		{"id":"t1","name":"Research","agent":"research","input":"find X","depends_on":[]},
		{"id":"t2","name":"Summarize","agent":"general","input":"summarize","depends_on":["t1"]}
	]}`}
	tasks, err := NewPlanner(gw).Plan(context.Background(), "investigate X")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks[0].AgentType != "research" || tasks[1].DependsOn[0] != "t1" {
		t.Errorf("tasks = %+v", tasks)
	}
}

func TestPlan_EmptyGoalErrors(t *testing.T) {
	if _, err := NewPlanner(planGateway{}).Plan(context.Background(), "  "); err == nil {
		t.Error("empty goal should error")
	}
}

func TestPlan_FallbackWithoutAI(t *testing.T) {
	tasks, err := NewPlanner(nil).Plan(context.Background(), "do a thing")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].AgentType != "general" || tasks[0].Input != "do a thing" {
		t.Errorf("fallback = %+v", tasks)
	}
}

func TestPlan_FallbackOnAIError(t *testing.T) {
	gw := planGateway{err: fmt.Errorf("provider down")}
	tasks, err := NewPlanner(gw).Plan(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].AgentType != "general" {
		t.Errorf("AI error should fall back to a single task: %+v", tasks)
	}
}

func TestPlan_FallbackOnUnparseable(t *testing.T) {
	tasks, err := NewPlanner(planGateway{reply: "sorry, no JSON here"}).Plan(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Errorf("unparseable reply should fall back, got %d tasks", len(tasks))
	}
}

func TestPlan_ParsesMaxAttemptsAndVerification(t *testing.T) {
	gw := planGateway{reply: `{"tasks":[
		{"id":"t1","agent":"general","input":"do","depends_on":[],"max_attempts":2,"verification_criteria":"contains:ok"}
	]}`}
	tasks, err := NewPlanner(gw).Plan(context.Background(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len=%d", len(tasks))
	}
	if tasks[0].MaxAttempts != 2 || tasks[0].VerificationCriteria != "contains:ok" {
		t.Errorf("got %+v", tasks[0])
	}
}

func TestPlan_RejectsUnsupportedVerificationCriteria(t *testing.T) {
	gw := planGateway{reply: `{"tasks":[{"id":"t1","agent":"general","input":"do","verification_criteria":"tests pass"}]}`}
	_, err := NewPlanner(gw).Plan(context.Background(), "goal")
	if err == nil || !strings.Contains(err.Error(), "unsupported verification_criteria") {
		t.Fatalf("free-text criteria should be rejected at plan time: %v", err)
	}
}

func TestValidatePlan_RejectsDuplicateID(t *testing.T) {
	err := validatePlan([]*Task{
		{ID: "t1", AgentType: "general"},
		{ID: "t1", AgentType: "general"},
	})
	if err == nil {
		t.Error("duplicate ID should be rejected")
	}
}

func TestValidatePlan_RejectsUnknownAgent(t *testing.T) {
	err := validatePlan([]*Task{{ID: "t1", AgentType: "wizard"}})
	if err == nil {
		t.Error("unknown agent type should be rejected")
	}
}

func TestValidatePlan_RejectsUnknownDependency(t *testing.T) {
	err := validatePlan([]*Task{{ID: "t1", AgentType: "general", DependsOn: []string{"ghost"}}})
	if err == nil {
		t.Error("dependency on an unknown task should be rejected")
	}
}

func TestValidatePlan_RejectsCycle(t *testing.T) {
	err := validatePlan([]*Task{
		{ID: "a", AgentType: "general", DependsOn: []string{"b"}},
		{ID: "b", AgentType: "general", DependsOn: []string{"a"}},
	})
	if err == nil {
		t.Error("a dependency cycle should be rejected")
	}
}

func TestPlan_RejectsInvalidPlan(t *testing.T) {
	// AI returns a plan with an unknown agent → Plan should error (not fall back).
	gw := planGateway{reply: `{"tasks":[{"id":"t1","agent":"wizard","input":"x"}]}`}
	if _, err := NewPlanner(gw).Plan(context.Background(), "goal"); err == nil {
		t.Error("an invalid plan should error")
	}
}

func TestPlan_ResolvesRequiredCapabilities(t *testing.T) {
	gw := planGateway{reply: `{"tasks":[
		{"id":"t1","required_capabilities":["research.web"],"input":"look up","depends_on":[]}
	]}`}
	tasks, err := NewPlanner(gw).Plan(context.Background(), "goal")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(tasks) != 1 || tasks[0].AgentType != "research" {
		t.Fatalf("got %+v", tasks[0])
	}
}

func TestPlan_RequiredCapabilitiesNoMatchErrors(t *testing.T) {
	gw := planGateway{reply: `{"tasks":[
		{"id":"t1","required_capabilities":["no.such.capability"],"input":"x","depends_on":[]}
	]}`}
	_, err := NewPlanner(gw).Plan(context.Background(), "goal")
	if err == nil || !strings.Contains(err.Error(), "no agent matches") {
		t.Fatalf("want no-match error, got %v", err)
	}
}

func TestPlan_RequiredCapabilitiesPermissionProfile(t *testing.T) {
	gw := planGateway{reply: `{"tasks":[
		{"id":"t1","required_capabilities":["code.read"],"permission_profile":"read-mostly","input":"review","depends_on":[]}
	]}`}
	_, err := NewPlanner(gw).Plan(context.Background(), "goal")
	if err == nil || !strings.Contains(err.Error(), "team-only") {
		t.Fatalf("review agent is team-only for orchestration: %v", err)
	}
}

func TestPlan_RequiredCapabilitiesAndAgentMutuallyExclusive(t *testing.T) {
	gw := planGateway{reply: `{"tasks":[
		{"id":"t1","agent":"general","required_capabilities":["general.chat"],"input":"x","depends_on":[]}
	]}`}
	_, err := NewPlanner(gw).Plan(context.Background(), "goal")
	if err == nil || !strings.Contains(err.Error(), "both agent and required_capabilities") {
		t.Fatalf("want mutual exclusion error, got %v", err)
	}
}

func TestPlan_TeamOnlyCapabilityDoesNotFallback(t *testing.T) {
	gw := planGateway{reply: `{"tasks":[
		{"id":"t1","required_capabilities":["code.edit"],"input":"patch","depends_on":[]}
	]}`}
	_, err := NewPlanner(gw).Plan(context.Background(), "goal")
	if err == nil || !strings.Contains(err.Error(), "team-only") {
		t.Fatalf("code.edit must not fall back to general: %v", err)
	}
}

func TestPlan_CustomCatalogForCapabilityMatch(t *testing.T) {
	cat, err := a2a.NewCatalog([]a2a.AgentManifest{
		{ID: "research-agent", Name: "R", Role: "researcher", Version: "1.0.0",
			Capabilities: []string{"research.web"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	gw := planGateway{reply: `{"tasks":[{"id":"t1","required_capabilities":["research.web"],"input":"x"}]}`}
	pl := NewPlanner(gw)
	pl.SetCatalog(cat)
	tasks, err := pl.Plan(context.Background(), "g")
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].AgentType != "research" {
		t.Fatalf("got %q", tasks[0].AgentType)
	}
}

func TestPlan_LoadableIntoQueue(t *testing.T) {
	gw := planGateway{reply: `{"tasks":[
		{"id":"t1","agent":"research","input":"a","depends_on":[]},
		{"id":"t2","agent":"general","input":"b","depends_on":["t1"]}
	]}`}
	tasks, err := NewPlanner(gw).Plan(context.Background(), "g")
	if err != nil {
		t.Fatal(err)
	}
	q := NewTaskQueue()
	for _, tk := range tasks {
		if err := q.Add(tk); err != nil {
			t.Fatalf("queue load: %v", err)
		}
	}
	first := q.Claim()
	if first == nil || first.ID != "t1" {
		t.Errorf("first claimable should be t1, got %+v", first)
	}
}
