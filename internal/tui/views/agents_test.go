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
	updated, cmd := m.Update(agentResponse{content: "[APPROVAL_REQUIRED] write file x"})
	am := updated.(AgentsModel)
	if !am.Awaiting() {
		t.Error("an [APPROVAL_REQUIRED] reply should enter awaiting state")
	}
	// Y/N must NOT be active yet (race guard) until the readiness tick fires.
	if am.ApprovalReady() {
		t.Error("approval should not be ready immediately (race guard)")
	}
	if cmd == nil {
		t.Error("entering awaiting should schedule the readiness tick")
	}
	out := am.renderMessages()
	if !strings.Contains(out, "[Y]") || !strings.Contains(out, "Enter to approve") {
		t.Errorf("approval reply should show the Y/N+Enter prompt:\n%s", out)
	}
	if strings.Contains(out, "[APPROVAL_REQUIRED]") {
		t.Error("raw [APPROVAL_REQUIRED] marker should be rewritten")
	}
}

func TestAgents_KeysIgnoredBeforeReady(t *testing.T) {
	m := NewAgents(nil)
	updated, _ := m.Update(agentResponse{content: "[APPROVAL_REQUIRED] run command"})
	am := updated.(AgentsModel) // NOT ready yet
	// A 'y' before the readiness tick must NOT stage or send anything.
	staged, cmd := am.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if staged.(AgentsModel).ApprovalChoice() != "" {
		t.Error("Y before ready should not stage a choice")
	}
	if cmd != nil {
		t.Error("Y before ready should not send anything")
	}
}

func TestAgents_StageThenEnterApproves(t *testing.T) {
	m := NewAgents(nil)
	m, _ = applyApprovalPending(t, m)
	// Y stages but does NOT send.
	staged, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	am := staged.(AgentsModel)
	if cmd != nil {
		t.Error("Y should only stage, not send")
	}
	if am.ApprovalChoice() != "approve" {
		t.Errorf("choice = %q, want approve", am.ApprovalChoice())
	}
	if !strings.Contains(am.renderMessages(), "Approve selected") {
		t.Errorf("staged approve should be shown:\n%s", am.renderMessages())
	}
	// Enter confirms and sends.
	_, cmd2 := am.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd2 == nil {
		t.Fatal("Enter after staging Y should send the approval")
	}
	if res := cmd2().(approvalResult); !res.approved {
		t.Error("staged Y + Enter should approve")
	}
}

func TestAgents_StageThenEnterRejects(t *testing.T) {
	m := NewAgents(nil)
	m, _ = applyApprovalPending(t, m)
	staged, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	_, cmd := staged.(AgentsModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter after staging N should send the rejection")
	}
	if res := cmd().(approvalResult); res.approved {
		t.Error("staged N + Enter should reject")
	}
}

func TestAgents_EnterWithoutStageDoesNothing(t *testing.T) {
	m := NewAgents(nil)
	m, _ = applyApprovalPending(t, m)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("Enter with no staged choice should do nothing")
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

// applyApprovalPending drives the model into the awaiting state AND fires the
// readiness tick (the 100ms guard) so Y/N are accepted — the common test setup.
func applyApprovalPending(t *testing.T, m AgentsModel) (AgentsModel, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(agentResponse{content: "[APPROVAL_REQUIRED] run command"})
	am := updated.(AgentsModel)
	if !am.Awaiting() {
		t.Fatal("setup: model should be awaiting")
	}
	ready, _ := am.Update(approvalReadyMsg{})
	return ready.(AgentsModel), cmd
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

func TestAgents_ApprovalResultSplitsOutputLines(t *testing.T) {
	m := NewAgents(nil)
	m, _ = applyApprovalPending(t, m)
	// A multi-line result (command output) should become separate agent lines.
	updated, _ := m.Update(approvalResult{approved: true, result: "line one\nline two\n✓ Completed (exit 0)"})
	am := updated.(AgentsModel)
	msgs := am.Messages()
	// Expect: …, system "✓ Action approved", agent "line one", agent "line two", agent "✓ Completed…"
	var agentLines []string
	for _, msg := range msgs {
		if msg.Role == "agent" {
			agentLines = append(agentLines, msg.Content)
		}
	}
	got := strings.Join(agentLines, "|")
	if !strings.Contains(got, "line one") || !strings.Contains(got, "line two") || !strings.Contains(got, "Completed") {
		t.Errorf("output should be split into separate agent lines, got: %s", got)
	}
	// Each output line is its own message (not one blob).
	if !containsExact(agentLines, "line one") || !containsExact(agentLines, "line two") {
		t.Errorf("each line should be a distinct message: %v", agentLines)
	}
}

func containsExact(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func TestOrderTranscript_OutputBeforeCompletion(t *testing.T) {
	// Even if the completion marker came first in the raw transcript, output
	// must render before it.
	got := orderTranscript("✓ Completed (exit 0)\nhello from calc\nsecond line")
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3: %v", len(got), got)
	}
	if got[0] != "hello from calc" || got[1] != "second line" {
		t.Errorf("output should come first: %v", got)
	}
	if got[len(got)-1] != "✓ Completed (exit 0)" {
		t.Errorf("completion should be last: %v", got)
	}
}

func TestOrderTranscript_StripsBlankLines(t *testing.T) {
	got := orderTranscript("a\n\n\nb\n")
	if len(got) != 2 {
		t.Errorf("blank lines should be dropped: %v", got)
	}
}

func TestApprovalResult_RunOutputOrderInChat(t *testing.T) {
	m := NewAgents(nil)
	m, _ = applyApprovalPending(t, m)
	updated, _ := m.Update(approvalResult{approved: true, result: "hello from calc\n✓ Completed (exit 0)"})
	var agentLines []string
	for _, msg := range updated.(AgentsModel).Messages() {
		if msg.Role == "agent" {
			agentLines = append(agentLines, msg.Content)
		}
	}
	// stdout must appear before the completion line.
	hi, ci := -1, -1
	for i, l := range agentLines {
		if l == "hello from calc" {
			hi = i
		}
		if strings.Contains(l, "Completed") {
			ci = i
		}
	}
	if hi < 0 || ci < 0 || hi > ci {
		t.Errorf("stdout should appear before completion: %v", agentLines)
	}
}

func TestRenderQuestions_NumberedOptions(t *testing.T) {
	qs := []tui.ForgeQuestion{
		{Question: "What type of calculator?", Key: "calc", Options: []string{"Basic", "Scientific"}},
		{Question: "Target platform?", Key: "plat", Options: []string{"Phone", "Tablet"}},
	}
	out := renderQuestions(qs)
	for _, want := range []string{"1. What type of calculator?", "[1] Basic", "[2] Scientific", "2. Target platform?", "[1] Phone", "numbers separated by space"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered questions missing %q:\n%s", want, out)
		}
	}
}

func TestParseOptionAnswer_Numbers(t *testing.T) {
	qs := []tui.ForgeQuestion{
		{Options: []string{"Basic", "Scientific"}},
		{Options: []string{"Phone", "Tablet"}},
	}
	ans, free := parseOptionAnswer("2 1", qs)
	if free {
		t.Error("'2 1' should parse as a numeric selection")
	}
	if ans != "Scientific, Phone" {
		t.Errorf("answer = %q, want 'Scientific, Phone'", ans)
	}
}

func TestParseOptionAnswer_FreeTextFallback(t *testing.T) {
	qs := []tui.ForgeQuestion{{Options: []string{"A", "B"}}}
	// Words → free text.
	if ans, free := parseOptionAnswer("a basic one please", qs); !free || ans != "a basic one please" {
		t.Errorf("free text = %q, free=%v", ans, free)
	}
	// Out-of-range number → free text.
	if _, free := parseOptionAnswer("9", qs); !free {
		t.Error("out-of-range number should fall back to free text")
	}
}

func TestForgeProgress_NeedsClarificationRendersOptions(t *testing.T) {
	m := NewAgents(nil)
	m.forgeJob = "job-1"
	upd, _ := m.Update(forgeProgress{job: &tui.ForgeJobData{
		ID: "job-1", State: "needs_clarification",
		Questions: []tui.ForgeQuestion{{Question: "Basic or scientific?", Options: []string{"Basic", "Scientific"}}},
	}})
	am := upd.(AgentsModel)
	if !am.PendingQuestions() {
		t.Error("needs_clarification should set pending questions")
	}
	if am.ForgePolling() {
		t.Error("should stop polling while awaiting an answer")
	}
	if !strings.Contains(am.renderMessages(), "[1] Basic") {
		t.Errorf("options should be rendered:\n%s", am.renderMessages())
	}
}

func TestAgents_AnswerMapsOptionsAndSubmits(t *testing.T) {
	m := NewAgents(nil)
	m.pendingQs = []tui.ForgeQuestion{
		{Options: []string{"Basic", "Scientific"}},
		{Options: []string{"Phone", "Tablet"}},
	}
	m.input.SetValue("2 1")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	am := updated.(AgentsModel)
	if am.PendingQuestions() {
		t.Error("pending questions should be cleared after answering")
	}
	if cmd == nil {
		t.Fatal("answering should submit")
	}
	// The user message shows what they typed; the submit carries the mapped text.
	last := am.Messages()[len(am.Messages())-1]
	if last.Content != "2 1" {
		t.Errorf("user message should show the typed answer, got %q", last.Content)
	}
}

func TestAgents_IsInputFocusedAlways(t *testing.T) {
	m := NewAgents(nil)
	if !m.IsInputFocused() {
		t.Error("Agents chat input should always be focused")
	}
}

func TestAgents_TabDisabledDuringOptions(t *testing.T) {
	m := NewAgents(nil)
	m.pendingQs = []tui.ForgeQuestion{{Options: []string{"A", "B"}}}
	m.input.SetValue("/sea")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	// Tab must NOT autocomplete while answering options — input unchanged.
	if updated.(AgentsModel).input.Value() != "/sea" {
		t.Errorf("Tab should be inert during option selection, got %q", updated.(AgentsModel).input.Value())
	}
}

func TestAgents_TabAutocompletesNormally(t *testing.T) {
	m := NewAgents(nil) // no pending questions
	m.input.SetValue("/sea")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if updated.(AgentsModel).input.Value() != "/search " {
		t.Errorf("Tab should autocomplete /sea → /search, got %q", updated.(AgentsModel).input.Value())
	}
}

func TestAgents_OptionPromptShown(t *testing.T) {
	m := NewAgents(nil)
	m.pendingQs = []tui.ForgeQuestion{{Question: "x?", Options: []string{"A"}}}
	if !strings.Contains(m.View(), "Enter numbers") {
		t.Errorf("option-selection view should show the numbered-entry prompt:\n%s", m.View())
	}
}
