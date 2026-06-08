package views

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
)

func TestAgents_Init(t *testing.T) {
	m := NewAgents(nil)
	if m.Init() == nil {
		t.Error("Init should return the spinner tick command")
	}
	// Starts with a system greeting.
	if len(m.Messages()) != 1 || m.Messages()[0].Role != "system" {
		t.Errorf("expected one system message, got %+v", m.Messages())
	}
}

func TestAgents_ViewHasInputPrompt(t *testing.T) {
	m := NewAgents(nil)
	if !strings.Contains(m.View(), ">") {
		t.Errorf("view should show the input prompt, got:\n%s", m.View())
	}
}

func TestAgents_EnterAddsUserMessageAndThinks(t *testing.T) {
	m := NewAgents(nil)
	m.input.SetValue("build me a go app")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	am := updated.(AgentsModel)

	// A user message was appended and input cleared.
	last := am.Messages()[len(am.Messages())-1]
	if last.Role != "user" || last.Content != "build me a go app" {
		t.Errorf("expected user message, got %+v", last)
	}
	if am.input.Value() != "" {
		t.Errorf("input should be cleared after send, got %q", am.input.Value())
	}
	if !am.Thinking() {
		t.Error("model should be thinking after submit")
	}
	if cmd == nil {
		t.Error("Enter should return a submit command")
	}
}

func TestAgents_ResponseClearsThinking(t *testing.T) {
	m := NewAgents(nil)
	m.thinking = true
	updated, _ := m.Update(agentResponse{content: "done"})
	am := updated.(AgentsModel)
	if am.Thinking() {
		t.Error("response should clear thinking")
	}
	last := am.Messages()[len(am.Messages())-1]
	if last.Role != "agent" || last.Content != "done" {
		t.Errorf("expected agent reply, got %+v", last)
	}
}

func TestAgents_RoleStyling(t *testing.T) {
	m := NewAgents(nil)
	m.messages = append(m.messages,
		ChatMessage{Role: "user", Content: "hi"},
		ChatMessage{Role: "agent", Content: "hello"},
	)
	out := m.renderMessages()
	if !strings.Contains(out, "[user] hi") || !strings.Contains(out, "[agent] hello") {
		t.Errorf("messages should be role-tagged, got:\n%s", out)
	}
	if !strings.Contains(out, "[system]") {
		t.Error("system greeting should still render")
	}
}

func TestAgents_ClearChat(t *testing.T) {
	m := NewAgents(nil)
	m.messages = append(m.messages, ChatMessage{Role: "user", Content: "x"})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	am := updated.(AgentsModel)
	if len(am.Messages()) != 1 {
		t.Errorf("Ctrl+L should keep only the system greeting, got %d", len(am.Messages()))
	}
}

func TestAgents_Autocomplete(t *testing.T) {
	if got := autocomplete("buil"); got != "build me a " {
		t.Errorf("autocomplete(buil) = %q, want 'build me a '", got)
	}
	if got := autocomplete(""); got != "build me a " {
		t.Errorf("autocomplete(empty) = %q, want first completion", got)
	}
	if got := autocomplete("xyz"); got != "xyz" {
		t.Errorf("autocomplete(no match) = %q, want unchanged", got)
	}
}

func TestAgents_EnterIgnoredWhileThinking(t *testing.T) {
	m := NewAgents(nil)
	m.thinking = true
	m.input.SetValue("another")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	am := updated.(AgentsModel)
	// No new user message added while thinking.
	for _, msg := range am.Messages() {
		if msg.Content == "another" {
			t.Error("input should be ignored while thinking")
		}
	}
}

func TestAgents_ApprovalRequiredEntersAwaiting(t *testing.T) {
	m := NewAgents(nil)
	m.thinking = true
	updated, _ := m.Update(agentResponse{content: "[APPROVAL_REQUIRED] write file x"})
	am := updated.(AgentsModel)
	if !am.Awaiting() {
		t.Error("an [APPROVAL_REQUIRED] reply should enter awaiting state")
	}
	out := am.renderMessages()
	if !strings.Contains(out, "[Y] Approve") || !strings.Contains(out, "[N] Reject") {
		t.Errorf("approval reply should show Y/N prompt:\n%s", out)
	}
	// The raw marker should not leak.
	if strings.Contains(out, "[APPROVAL_REQUIRED]") {
		t.Error("raw [APPROVAL_REQUIRED] marker should be rewritten")
	}
}

func TestAgents_ApproveKeySendsDecision(t *testing.T) {
	m := NewAgents(nil)
	m, _ = applyApprovalPending(t, m)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("Y should send an approval command")
	}
	msg := cmd()
	res, ok := msg.(approvalResult)
	if !ok || !res.approved {
		t.Errorf("Y should produce approvalResult{approved:true}, got %#v", msg)
	}
}

func TestAgents_RejectKeySendsDecision(t *testing.T) {
	m := NewAgents(nil)
	m, _ = applyApprovalPending(t, m)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if cmd == nil {
		t.Fatal("N should send a rejection command")
	}
	if res := cmd().(approvalResult); res.approved {
		t.Error("N should produce approvalResult{approved:false}")
	}
}

func TestAgents_AwaitingIgnoresTyping(t *testing.T) {
	m := NewAgents(nil)
	m, _ = applyApprovalPending(t, m)
	// A normal letter key while awaiting should not type into the input.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if updated.(AgentsModel).input.Value() != "" {
		t.Error("typing should be ignored while awaiting approval")
	}
}

func TestAgents_ApprovalResultClearsAwaiting(t *testing.T) {
	m := NewAgents(nil)
	m, _ = applyApprovalPending(t, m)
	updated, _ := m.Update(approvalResult{approved: true})
	am := updated.(AgentsModel)
	if am.Awaiting() {
		t.Error("approvalResult should clear awaiting")
	}
	last := am.Messages()[len(am.Messages())-1]
	if last.Role != "system" || !strings.Contains(last.Content, "approved") {
		t.Errorf("expected an 'approved' system line, got %+v", last)
	}
}

func TestAgents_SlashAutocomplete(t *testing.T) {
	if got := autocomplete("/l"); got != "/ls " {
		t.Errorf("autocomplete(/l) = %q, want '/ls '", got)
	}
	if got := autocomplete("/pro"); got != "/project " {
		t.Errorf("autocomplete(/pro) = %q, want '/project '", got)
	}
	// Non-slash still uses the prose completions.
	if got := autocomplete("buil"); got != "build me a " {
		t.Errorf("autocomplete(buil) = %q, want 'build me a '", got)
	}
}

// applyApprovalPending drives the model into the awaiting state.
func applyApprovalPending(t *testing.T, m AgentsModel) (AgentsModel, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(agentResponse{content: "[APPROVAL_REQUIRED] run command"})
	am := updated.(AgentsModel)
	if !am.Awaiting() {
		t.Fatal("setup: model should be awaiting")
	}
	return am, cmd
}

func TestAgents_ExtractJobID(t *testing.T) {
	if got := extractJobID("Starting build... Job ID: job-abc123"); got != "job-abc123" {
		t.Errorf("extractJobID = %q, want job-abc123", got)
	}
	if got := extractJobID("no job here"); got != "" {
		t.Errorf("extractJobID(no match) = %q, want empty", got)
	}
}

func TestAgents_ForgeProgressAppendsNewSteps(t *testing.T) {
	m := NewAgents(nil)
	m.forgeJob = "job-1"
	// First poll: two steps.
	upd, _ := m.Update(forgeProgress{job: &tui.ForgeJobData{
		ID: "job-1", State: "running",
		ProgressHistory: []string{"Parsing intent…", "Generating code…"},
	}})
	am := upd.(AgentsModel)
	out := am.renderMessages()
	if !strings.Contains(out, "Parsing intent") || !strings.Contains(out, "Generating code") {
		t.Errorf("forge steps should appear:\n%s", out)
	}
	if !am.ForgePolling() {
		t.Error("should keep polling while running")
	}

	// Second poll: only the new third step is appended (no duplicates).
	upd2, _ := am.Update(forgeProgress{job: &tui.ForgeJobData{
		ID: "job-1", State: "running",
		ProgressHistory: []string{"Parsing intent…", "Generating code…", "Building…"},
	}})
	out2 := upd2.(AgentsModel).renderMessages()
	if strings.Count(out2, "Parsing intent") != 1 {
		t.Errorf("steps should not duplicate across polls:\n%s", out2)
	}
	if !strings.Contains(out2, "Building…") {
		t.Errorf("new step should appear:\n%s", out2)
	}
}

func TestAgents_ForgeCompleteStopsPolling(t *testing.T) {
	m := NewAgents(nil)
	m.forgeJob = "job-1"
	upd, cmd := m.Update(forgeProgress{job: &tui.ForgeJobData{
		ID: "job-1", State: "complete", Result: "Build complete: calc", DurationMs: 45000,
	}})
	am := upd.(AgentsModel)
	if am.ForgePolling() {
		t.Error("should stop polling when complete")
	}
	if cmd != nil {
		t.Error("no further poll command after completion")
	}
	out := am.renderMessages()
	if !strings.Contains(out, "Build complete: calc") || !strings.Contains(out, "45.0s") {
		t.Errorf("completion should show result + duration:\n%s", out)
	}
}

func TestAgents_ForgeFailedShowsError(t *testing.T) {
	m := NewAgents(nil)
	m.forgeJob = "job-1"
	upd, _ := m.Update(forgeProgress{job: &tui.ForgeJobData{
		ID: "job-1", State: "failed", Error: "compile error",
	}})
	am := upd.(AgentsModel)
	if am.ForgePolling() {
		t.Error("should stop polling when failed")
	}
	if !strings.Contains(am.renderMessages(), "Build failed: compile error") {
		t.Errorf("failure should show the error:\n%s", am.renderMessages())
	}
}

func TestAgents_ForgePollErrorStops(t *testing.T) {
	m := NewAgents(nil)
	m.forgeJob = "job-1"
	upd, cmd := m.Update(forgeProgress{err: errString("network")})
	if upd.(AgentsModel).ForgePolling() {
		t.Error("a poll error should stop polling")
	}
	if cmd != nil {
		t.Error("no further poll after error")
	}
}

func TestAgents_ThinkingShownImmediatelyOnSubmit(t *testing.T) {
	m := NewAgents(nil)
	m.input.SetValue("hello")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	am := updated.(AgentsModel)
	if !am.Thinking() {
		t.Fatal("model should be thinking right after submit")
	}
	// The transcript should already show an animated Thinking line.
	if !strings.Contains(am.renderMessages(), "Thinking") {
		t.Errorf("a Thinking indicator should appear immediately:\n%s", am.renderMessages())
	}
	if cmd == nil {
		t.Error("submit should also kick the spinner/submit commands")
	}
}

func TestAgents_SpinnerTickRefreshesWhileThinking(t *testing.T) {
	m := NewAgents(nil)
	m.thinking = true
	// A spinner tick while thinking should return a follow-up tick command so
	// the animation keeps running (and the transcript is rebuilt each frame).
	_, cmd := m.Update(spinner.TickMsg{})
	if cmd == nil {
		t.Error("spinner tick while thinking should schedule the next frame")
	}
}

func TestAgents_ResponseReplacesThinking(t *testing.T) {
	m := NewAgents(nil)
	m.thinking = true
	updated, _ := m.Update(agentResponse{content: "the real answer"})
	am := updated.(AgentsModel)
	if am.Thinking() {
		t.Error("response should clear thinking")
	}
	out := am.renderMessages()
	if !strings.Contains(out, "the real answer") {
		t.Errorf("response should replace the Thinking line:\n%s", out)
	}
	if strings.Contains(out, "Thinking…") {
		t.Errorf("Thinking indicator should be gone after the response:\n%s", out)
	}
}
