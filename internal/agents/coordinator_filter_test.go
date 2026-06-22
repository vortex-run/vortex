package agents

import (
	"strings"
	"testing"
)

func TestFilterCoordinatorResponse_StripsSkillBlock(t *testing.T) {
	raw := strings.Join([]string{
		"I'll build a Flask calculator web app. Starting the agent team now.",
		"",
		"You have a proven skill for this type of task:",
		"assess_user_goal: Analyzes a user query...",
		"Steps:",
		"1. Plan the work (tool: plan)",
		"2. Execute (tool: run)",
		"Use this procedure unless the user asks for something different.",
	}, "\n")

	got := filterCoordinatorResponse(raw)
	for _, banned := range []string{"proven skill", "assess_user_goal", "Steps:", "(tool:", "Use this procedure"} {
		if strings.Contains(got, banned) {
			t.Errorf("filtered output still contains %q:\n%s", banned, got)
		}
	}
	if !strings.Contains(got, "I'll build a Flask calculator") {
		t.Errorf("filter dropped the real answer:\n%s", got)
	}
}

func TestFilterCoordinatorResponse_StripsTaskMetricsAndGoalEcho(t *testing.T) {
	raw := strings.Join([]string{
		"coordinator: Goal: create me a calculator web app",
		"Building your calculator now.",
		"2 tasks: 2 completed, 0 failed (14.616s)",
		"✅ Build Python calculator web app",
	}, "\n")

	got := filterCoordinatorResponse(raw)
	if strings.Contains(got, "Goal:") {
		t.Errorf("goal echo not stripped:\n%s", got)
	}
	if strings.Contains(got, "tasks:") || strings.Contains(got, "completed") {
		t.Errorf("task metrics not stripped:\n%s", got)
	}
	if !strings.Contains(got, "Building your calculator now.") {
		t.Errorf("filter dropped the real answer:\n%s", got)
	}
}

func TestFilterCoordinatorResponse_KeepsCleanAnswer(t *testing.T) {
	// A normal answer (including user-facing numbered prose) must pass untouched.
	raw := strings.Join([]string{
		"Here are three good options:",
		"1. Flask — simple and lightweight",
		"2. Django — full-featured",
		"3. FastAPI — modern and async",
	}, "\n")
	got := filterCoordinatorResponse(raw)
	if got != raw {
		t.Errorf("clean answer was altered:\nwant:\n%s\ngot:\n%s", raw, got)
	}
}

func TestFilterCoordinatorResponse_KeepsQuestionOptions(t *testing.T) {
	// The QUESTION/OPTIONS selector format must survive filtering.
	raw := "QUESTION: What framework should I use?\nOPTIONS:\n- Flask (simple)\n- Django (full)\n- FastAPI (async)"
	got := filterCoordinatorResponse(raw)
	if !strings.Contains(got, "QUESTION:") || !strings.Contains(got, "OPTIONS:") || !strings.Contains(got, "- Flask") {
		t.Errorf("QUESTION/OPTIONS block was damaged:\n%s", got)
	}
}
