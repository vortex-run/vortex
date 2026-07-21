package orchestration

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/vortex-run/vortex/internal/agents"
)

// TestRun_DurableRunsCarryEffectScope proves that when durability is enabled
// (Store + RunID), every task execution carries an effect scope of
// "<runID>/<taskID>" in its context, and that non-durable runs carry none
// (production audit H3 increment 2).
func TestRun_DurableRunsCarryEffectScope(t *testing.T) {
	store, err := NewRunStore(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	var (
		mu     sync.Mutex
		scopes = map[string]string{}
	)
	exec := ExecutorFunc(func(ctx context.Context, task *Task, _ *SharedMemory) (string, error) {
		scope, _ := agents.EffectScope(ctx)
		mu.Lock()
		scopes[task.ID] = scope
		mu.Unlock()
		return "ok", nil
	})

	tasks := []*Task{
		{ID: "a", Name: "A", AgentType: "research"},
		{ID: "b", Name: "B", AgentType: "devops", DependsOn: []string{"a"}},
	}
	runID, err := store.CreateRun("goal", "s1", tasks)
	if err != nil {
		t.Fatal(err)
	}
	o := newOrch(t, exec, func(c *OrchestratorConfig) {
		c.Store = store
		c.RunID = runID
	})
	if _, err := o.Run(context.Background(), "goal", tasks); err != nil {
		t.Fatal(err)
	}
	if scopes["a"] != runID+"/a" || scopes["b"] != runID+"/b" {
		t.Errorf("scopes = %v, want %s/a and %s/b", scopes, runID, runID)
	}

	// Non-durable run: no scope, so nothing is fenced.
	scopes = map[string]string{}
	o2 := newOrch(t, exec)
	if _, err := o2.Run(context.Background(), "goal", []*Task{{ID: "c", Name: "C", AgentType: "research"}}); err != nil {
		t.Fatal(err)
	}
	if scopes["c"] != "" {
		t.Errorf("non-durable run leaked scope %q", scopes["c"])
	}
}
