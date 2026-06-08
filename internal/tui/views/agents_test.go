package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
