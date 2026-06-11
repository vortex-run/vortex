package agents

import (
	"context"
	"errors"
	"testing"
)

// recordingGateway returns a fixed reply (or error) and records whether it was
// called — MaybeLearn's filters should often skip the AI call entirely.
type recordingGateway struct {
	reply  string
	err    error
	called bool
}

func (g *recordingGateway) Complete(_ context.Context, _, _ string) (string, error) {
	g.called = true
	return g.reply, g.err
}

const skillDocJSON = `{
  "name": "Create FastAPI app",
  "description": "Scaffold a FastAPI application",
  "triggers": ["fastapi", "python", "api"],
  "steps": [
    {"description": "Create project directory", "tool_name": "run_command"},
    {"description": "Write main.py", "tool_name": "write_file", "params": {"path": "main.py"}},
    {"description": "Write tests", "tool_name": "write_file", "optional": true}
  ]
}`

var learnSteps = []string{"created directory", "wrote main.py", "wrote tests"}

func TestSkillWriterShortTaskNotLearned(t *testing.T) {
	store := newTestSkillStore(t)
	gw := &recordingGateway{reply: skillDocJSON}
	w := NewSkillWriter(gw, store)

	if err := w.MaybeLearn(context.Background(), "make a file", []string{"wrote file", "done"}, "ok", true); err != nil {
		t.Fatalf("MaybeLearn: %v", err)
	}
	if gw.called {
		t.Error("AI called for a 2-step task")
	}
	if skills, _ := store.List(); len(skills) != 0 {
		t.Errorf("short task learned: %d skills stored", len(skills))
	}
}

func TestSkillWriterFailedTaskNotLearned(t *testing.T) {
	store := newTestSkillStore(t)
	gw := &recordingGateway{reply: skillDocJSON}
	w := NewSkillWriter(gw, store)

	if err := w.MaybeLearn(context.Background(), "build fastapi app", learnSteps, "build failed", false); err != nil {
		t.Fatalf("MaybeLearn: %v", err)
	}
	if gw.called {
		t.Error("AI called for a failed task")
	}
	if skills, _ := store.List(); len(skills) != 0 {
		t.Errorf("failed task learned: %d skills stored", len(skills))
	}
}

func TestSkillWriterDuplicateNotCreated(t *testing.T) {
	store := newTestSkillStore(t)
	if err := store.Save(sampleSkill()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	gw := &recordingGateway{reply: skillDocJSON}
	w := NewSkillWriter(gw, store)

	// Task text matches the existing skill's triggers via FTS.
	if err := w.MaybeLearn(context.Background(), "build a fastapi app", learnSteps, "done", true); err != nil {
		t.Fatalf("MaybeLearn: %v", err)
	}
	if gw.called {
		t.Error("AI called although a similar skill exists")
	}
	if skills, _ := store.List(); len(skills) != 1 {
		t.Errorf("duplicate created: %d skills stored, want 1", len(skills))
	}
}

func TestSkillWriterLearnsComplexSuccess(t *testing.T) {
	store := newTestSkillStore(t)
	gw := &recordingGateway{reply: "```json\n" + skillDocJSON + "\n```"} // fences must be stripped
	w := NewSkillWriter(gw, store)

	if err := w.MaybeLearn(context.Background(), "build a fastapi app", learnSteps, "app created", true); err != nil {
		t.Fatalf("MaybeLearn: %v", err)
	}
	if !gw.called {
		t.Fatal("AI not called for a learnable task")
	}
	skills, err := store.List()
	if err != nil || len(skills) != 1 {
		t.Fatalf("List = %d skills (%v), want 1", len(skills), err)
	}
	sk := skills[0]
	if sk.Name != "Create FastAPI app" {
		t.Errorf("Name = %q", sk.Name)
	}
	if len(sk.Steps) != 3 || sk.Steps[1].ToolName != "write_file" || !sk.Steps[2].IsOptional {
		t.Errorf("steps not preserved: %+v", sk.Steps)
	}
	if sk.CreatedFrom == "" {
		t.Error("CreatedFrom empty")
	}
	// Learned skill is now findable for the next similar task.
	if found, _ := store.Find("fastapi"); len(found) != 1 {
		t.Error("learned skill not findable via FTS")
	}
}

func TestSkillWriterAIErrorGraceful(t *testing.T) {
	store := newTestSkillStore(t)
	w := NewSkillWriter(&recordingGateway{err: errors.New("provider down")}, store)

	err := w.MaybeLearn(context.Background(), "build a fastapi app", learnSteps, "done", true)
	if err == nil {
		t.Fatal("expected error from AI failure")
	}
	if skills, _ := store.List(); len(skills) != 0 {
		t.Errorf("skill stored despite AI error: %d", len(skills))
	}
}

func TestSkillWriterBadJSONGraceful(t *testing.T) {
	store := newTestSkillStore(t)
	w := NewSkillWriter(&recordingGateway{reply: "sure, here's a skill!"}, store)

	if err := w.MaybeLearn(context.Background(), "build a fastapi app", learnSteps, "done", true); err == nil {
		t.Fatal("expected parse error for non-JSON reply")
	}
	if skills, _ := store.List(); len(skills) != 0 {
		t.Errorf("skill stored despite bad JSON: %d", len(skills))
	}
}
