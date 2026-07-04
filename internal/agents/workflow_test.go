package agents

import (
	"path/filepath"
	"testing"
)

func newTestWorkflowStore(t *testing.T) *WorkflowStore {
	t.Helper()
	store, err := NewWorkflowStore(filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatalf("NewWorkflowStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func plannedSteps() []WorkflowStep {
	return []WorkflowStep{
		{Description: "scaffold project", ToolName: "run_command", Params: map[string]any{"command": "mkdir"}},
		{Description: "write main.py", ToolName: "write_file"},
		{Description: "run tests", ToolName: "run_command"},
	}
}

func TestWorkflowCreatePersists(t *testing.T) {
	store := newTestWorkflowStore(t)
	wf, err := store.Create("build a fastapi app", "s1", plannedSteps())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if wf.ID == "" || wf.Status != WorkflowRunning {
		t.Fatalf("created workflow = %+v", wf)
	}

	loaded, err := store.load(wf.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Goal != "build a fastapi app" || loaded.SessionID != "s1" {
		t.Errorf("loaded = %+v", loaded)
	}
	if len(loaded.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(loaded.Steps))
	}
	st := loaded.Steps[0]
	if st.Status != StepPending || st.MaxRetries != 3 || st.ToolName != "run_command" {
		t.Errorf("step[0] = %+v (default MaxRetries should be 3)", st)
	}
	if cmd, _ := st.Params["command"].(string); cmd != "mkdir" {
		t.Errorf("params lost in round-trip: %v", st.Params)
	}

	if _, err := store.Create("  ", "s1", nil); err == nil {
		t.Error("Create with blank goal should error")
	}
}

func TestWorkflowUpdateStepPersistsResult(t *testing.T) {
	store := newTestWorkflowStore(t)
	wf, _ := store.Create("goal", "s1", plannedSteps())

	if err := store.UpdateStep(wf.ID, wf.Steps[0].ID, "scaffolded ok", ""); err != nil {
		t.Fatalf("UpdateStep: %v", err)
	}
	loaded, _ := store.load(wf.ID)
	if loaded.Steps[0].Status != StepDone || loaded.Steps[0].Result != "scaffolded ok" {
		t.Errorf("step[0] = %+v", loaded.Steps[0])
	}
	if loaded.CurrentStep != 1 {
		t.Errorf("CurrentStep = %d, want 1 after first step done", loaded.CurrentStep)
	}

	if err := store.UpdateStep(wf.ID, "no-such-step", "x", ""); err == nil {
		t.Error("UpdateStep on missing step should error")
	}
}

func TestWorkflowResumeReturnsNextStep(t *testing.T) {
	store := newTestWorkflowStore(t)
	wf, _ := store.Create("goal", "s1", plannedSteps())
	_ = store.UpdateStep(wf.ID, wf.Steps[0].ID, "done", "")
	_ = store.UpdateStep(wf.ID, wf.Steps[1].ID, "done", "")

	resumed, err := store.Resume(wf.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.CurrentStep != 2 {
		t.Errorf("CurrentStep = %d, want 2 (first incomplete step)", resumed.CurrentStep)
	}
	if resumed.Status != WorkflowRunning {
		t.Errorf("Status = %q, want running", resumed.Status)
	}
	if resumed.Steps[2].Description != "run tests" {
		t.Errorf("next step = %+v", resumed.Steps[resumed.CurrentStep])
	}
}

func TestWorkflowListIncomplete(t *testing.T) {
	store := newTestWorkflowStore(t)
	a, _ := store.Create("goal A", "s1", plannedSteps())
	b, _ := store.Create("goal B", "s2", nil)
	c, _ := store.Create("goal C", "s3", nil)

	if err := store.Complete(b.ID, "all done"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := store.Fail(c.ID, "exploded"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	incomplete, err := store.ListIncomplete()
	if err != nil {
		t.Fatalf("ListIncomplete: %v", err)
	}
	if len(incomplete) != 1 || incomplete[0].ID != a.ID {
		t.Fatalf("ListIncomplete = %d entries, want only goal A", len(incomplete))
	}
	if len(incomplete[0].Steps) != 3 {
		t.Errorf("incomplete workflow steps = %d, want 3 (loaded eagerly)", len(incomplete[0].Steps))
	}

	// Completed workflow keeps its result.
	done, _ := store.load(b.ID)
	if done.Status != WorkflowDone || done.Result != "all done" {
		t.Errorf("completed = %+v", done)
	}
}

func TestWorkflowFailedStepRetriedUpToMax(t *testing.T) {
	store := newTestWorkflowStore(t)
	wf, _ := store.Create("goal", "s1", plannedSteps())
	stepID := wf.Steps[0].ID

	// Two failures: attempts < MaxRetries(3), so the step returns to pending
	// (retryable) and the workflow stays running.
	for i := 0; i < 2; i++ {
		if err := store.UpdateStep(wf.ID, stepID, "", "boom"); err != nil {
			t.Fatalf("UpdateStep fail %d: %v", i, err)
		}
		loaded, _ := store.load(wf.ID)
		if loaded.Steps[0].Status != StepPending {
			t.Fatalf("after %d failures step status = %q, want pending (retryable)", i+1, loaded.Steps[0].Status)
		}
		if loaded.Status != WorkflowRunning {
			t.Fatalf("workflow status = %q, want running", loaded.Status)
		}
		// Resume points back at the failed step.
		resumed, _ := store.Resume(wf.ID)
		if resumed.CurrentStep != 0 {
			t.Fatalf("Resume CurrentStep = %d, want 0 (retry the failed step)", resumed.CurrentStep)
		}
	}

	// Third failure exhausts MaxRetries: step and workflow become failed.
	if err := store.UpdateStep(wf.ID, stepID, "", "boom"); err != nil {
		t.Fatalf("UpdateStep final fail: %v", err)
	}
	loaded, _ := store.load(wf.ID)
	if loaded.Steps[0].Status != StepFailed || loaded.Steps[0].Attempts != 3 {
		t.Errorf("step = %+v, want failed after 3 attempts", loaded.Steps[0])
	}
	if loaded.Status != WorkflowFailed {
		t.Errorf("workflow status = %q, want failed", loaded.Status)
	}
	if incomplete, _ := store.ListIncomplete(); len(incomplete) != 0 {
		t.Errorf("failed workflow still listed incomplete")
	}
}

func TestWorkflowAppendCompletedStep(t *testing.T) {
	store := newTestWorkflowStore(t)
	wf, _ := store.Create("dynamic goal", "s1", nil)

	for _, desc := range []string{"planned tasks", "built backend", "ran tests"} {
		if err := store.AppendCompletedStep(wf.ID, desc); err != nil {
			t.Fatalf("AppendCompletedStep: %v", err)
		}
	}
	loaded, _ := store.load(wf.ID)
	if len(loaded.Steps) != 3 || loaded.CurrentStep != 3 {
		t.Fatalf("steps = %d current = %d, want 3/3", len(loaded.Steps), loaded.CurrentStep)
	}
	if loaded.Steps[1].Description != "built backend" || loaded.Steps[1].Status != StepDone {
		t.Errorf("step[1] = %+v", loaded.Steps[1])
	}
}

// NOTE: the coordinator no longer records orchestrations in the WorkflowStore
// — durability moved to the orchestration package's RunStore, which persists
// the real task DAG and resumes at task granularity (production audit H3). See
// internal/orchestration/resume_test.go for the recovery coverage that
// replaced the coordinator-integration tests here.
