package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// recRouter records dispatched (agentType, input) pairs and returns a reply.
type recRouter struct {
	mu       sync.Mutex
	calls    []string // "agentType|input"
	replies  map[string]string
	failType string // agent type that should fail
}

func (r *recRouter) Dispatch(_ context.Context, agentType, input string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, agentType+"|"+input)
	if agentType == r.failType {
		return "", fmt.Errorf("%s failed", agentType)
	}
	if rep, ok := r.replies[agentType]; ok {
		return rep, nil
	}
	return "ok:" + agentType, nil
}

// orchNotifier records the orchestration notification.
type orchNotifier struct {
	mu       sync.Mutex
	notified bool
}

func (n *orchNotifier) Notify(context.Context, string, string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notified = true
	return nil
}

func TestAgent_RunNoRouterErrors(t *testing.T) {
	a := NewOrchestrationAgent(nil, nil, nil)
	if _, err := a.Run(context.Background(), "goal", nil); err == nil {
		t.Error("no router should error")
	}
}

func TestAgent_RunFallbackPlanSingleTask(t *testing.T) {
	// No gateway → planner falls back to one general task.
	router := &recRouter{}
	a := NewOrchestrationAgent(nil, router, nil)
	summary, err := a.Run(context.Background(), "do the thing", nil)
	if err != nil {
		t.Fatal(err)
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if len(router.calls) != 1 || !strings.HasPrefix(router.calls[0], "general|") {
		t.Errorf("expected one general dispatch, got %v", router.calls)
	}
	if !strings.Contains(summary, "do the thing") {
		t.Errorf("summary = %q", summary)
	}
}

func TestAgent_RunMultiTaskPlan(t *testing.T) {
	gw := planGateway{reply: `{"tasks":[
		{"id":"t1","name":"Research","agent":"research","input":"find X","depends_on":[]},
		{"id":"t2","name":"Report","agent":"general","input":"write up","depends_on":["t1"]}
	]}`}
	router := &recRouter{replies: map[string]string{"research": "found facts about X"}}
	a := NewOrchestrationAgent(gw, router, nil)
	summary, err := a.Run(context.Background(), "investigate X", nil)
	if err != nil {
		t.Fatal(err)
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if len(router.calls) != 2 {
		t.Fatalf("expected 2 dispatches, got %v", router.calls)
	}
	// research runs first.
	if !strings.HasPrefix(router.calls[0], "research|") {
		t.Errorf("first dispatch = %q", router.calls[0])
	}
	// The dependent task's input carries the upstream result.
	if !strings.Contains(router.calls[1], "found facts about X") {
		t.Errorf("dependent input missing upstream context: %q", router.calls[1])
	}
	if !strings.Contains(summary, "completed") {
		t.Errorf("summary = %q", summary)
	}
}

func TestAgent_RunNotifiesOnCompletion(t *testing.T) {
	notif := &orchNotifier{}
	a := NewOrchestrationAgent(nil, &recRouter{}, notif)
	if _, err := a.Run(context.Background(), "g", nil); err != nil {
		t.Fatal(err)
	}
	notif.mu.Lock()
	defer notif.mu.Unlock()
	if !notif.notified {
		t.Error("orchestration should notify on completion")
	}
}

func TestAgent_RunReportsFailures(t *testing.T) {
	gw := planGateway{reply: `{"tasks":[{"id":"t1","name":"Step","agent":"devops","input":"x","depends_on":[]}]}`}
	router := &recRouter{failType: "devops"}
	a := NewOrchestrationAgent(gw, router, nil)
	summary, err := a.Run(context.Background(), "g", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "❌") || !strings.Contains(summary, "1 failed") {
		t.Errorf("summary should report the failure: %q", summary)
	}
}

func TestAgent_DefaultsEmptyAgentTypeToGeneral(t *testing.T) {
	gw := planGateway{reply: `{"tasks":[{"id":"t1","name":"X","agent":"","input":"do","depends_on":[]}]}`}
	router := &recRouter{}
	a := NewOrchestrationAgent(gw, router, nil)
	if _, err := a.Run(context.Background(), "g", nil); err != nil {
		t.Fatal(err)
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if len(router.calls) != 1 || !strings.HasPrefix(router.calls[0], "general|") {
		t.Errorf("empty agent type should default to general: %v", router.calls)
	}
}

func TestAgent_ProgressEmitted(t *testing.T) {
	a := NewOrchestrationAgent(nil, &recRouter{}, nil)
	var steps []string
	var mu sync.Mutex
	_, err := a.Run(context.Background(), "g", func(s string) {
		mu.Lock()
		steps = append(steps, s)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(steps, " ")
	for _, want := range []string{"Planning", "Plan:", "Done"} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress missing %q: %s", want, joined)
		}
	}
}
