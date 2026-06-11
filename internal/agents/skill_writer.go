package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// skillWriterSystemPrompt asks the model to distil a completed task into a
// reusable procedure. The reply must be bare JSON (fences are stripped).
const skillWriterSystemPrompt = `Extract a reusable skill procedure from this completed task. Return only JSON, no prose:
{"name": "short human name", "description": "what this skill does", "triggers": ["keyword", ...], "steps": [{"description": "...", "tool_name": "...", "params": {}, "optional": false}, ...]}`

// minSkillSteps is the minimum number of steps a task must have taken before
// it is worth learning as a skill — anything shorter is cheaper to re-derive.
const minSkillSteps = 3

// SkillWriter turns completed multi-step tasks into stored skills (build plan
// upgrade 1 — self-improving agent). The coordinator calls MaybeLearn after
// every completed task; the writer decides whether the task is worth keeping.
type SkillWriter struct {
	gateway AIGateway
	store   *SkillStore
}

// NewSkillWriter constructs a SkillWriter over an AI gateway and skill store.
func NewSkillWriter(gateway AIGateway, store *SkillStore) *SkillWriter {
	return &SkillWriter{gateway: gateway, store: store}
}

// MaybeLearn inspects a completed task and, when it represents a successful
// multi-step procedure not already covered by an existing skill, asks the AI
// to distil it into a Skill and saves it. Failed tasks and short tasks are
// ignored (learning a failed approach would make the agent worse, and trivial
// tasks are cheaper to re-derive than to look up).
func (w *SkillWriter) MaybeLearn(ctx context.Context, task string, steps []string, result string, success bool) error {
	if !success || len(steps) < minSkillSteps {
		return nil
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return nil
	}
	// Don't relearn what we already know: an FTS hit on the task text means a
	// similar skill exists (and MarkUsed keeps its stats fresh instead).
	existing, err := w.store.Find(task)
	if err != nil {
		return fmt.Errorf("agents: checking for existing skill: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}

	prompt := fmt.Sprintf("Task: %s\nSteps taken:\n%s\nResult: %s",
		task, strings.Join(steps, "\n"), result)
	reply, err := w.gateway.Complete(ctx, prompt, skillWriterSystemPrompt)
	if err != nil {
		return fmt.Errorf("agents: generating skill: %w", err)
	}

	var doc struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Triggers    []string `json:"triggers"`
		Steps       []struct {
			Description string         `json:"description"`
			ToolName    string         `json:"tool_name"`
			Params      map[string]any `json:"params"`
			Optional    bool           `json:"optional"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(stripCodeFences(reply)), &doc); err != nil {
		return fmt.Errorf("agents: parsing skill JSON: %w", err)
	}
	if strings.TrimSpace(doc.Name) == "" || len(doc.Steps) == 0 {
		return fmt.Errorf("agents: AI returned incomplete skill document")
	}

	skill := &Skill{
		Name:        doc.Name,
		Description: doc.Description,
		Trigger:     doc.Triggers,
		CreatedFrom: truncateMemoryTitle(task),
	}
	for _, st := range doc.Steps {
		skill.Steps = append(skill.Steps, SkillStep{
			Description: st.Description,
			ToolName:    st.ToolName,
			Params:      st.Params,
			IsOptional:  st.Optional,
		})
	}
	if err := w.store.Save(skill); err != nil {
		return err
	}
	slog.Info("learned new skill", "name", skill.Name, "from_task", skill.CreatedFrom)
	return nil
}
