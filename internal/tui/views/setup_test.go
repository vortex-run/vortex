package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSetup_StartsAtWelcome(t *testing.T) {
	m := NewSetup()
	if m.Step() != StepWelcome {
		t.Errorf("step = %d, want StepWelcome", m.Step())
	}
	if !strings.Contains(m.View(), "First Time Setup") {
		t.Errorf("welcome view should show the banner:\n%s", m.View())
	}
}

func TestSetup_ProviderListHasFiveOptions(t *testing.T) {
	if len(setupProviders) != 5 {
		t.Errorf("provider list has %d options, want 5", len(setupProviders))
	}
}

func TestSetup_WelcomeAdvancesToProvider(t *testing.T) {
	m := NewSetup()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if updated.(SetupModel).Step() != StepProvider {
		t.Error("any key on welcome should advance to provider selection")
	}
}

func TestSetup_ProviderNavigateAndSelect(t *testing.T) {
	m := NewSetup()
	at, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // welcome → provider
	// Move down to DeepSeek (index 1) and select.
	down, _ := at.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	sel, _ := down.Update(tea.KeyMsg{Type: tea.KeyEnter})
	sm := sel.(SetupModel)
	if sm.SelectedProvider() != "deepseek" {
		t.Errorf("selected provider = %q, want deepseek", sm.SelectedProvider())
	}
	if sm.Step() != StepAPIKey {
		t.Errorf("a key-based provider should advance to API key step")
	}
}

func TestSetup_OllamaSkipsKeyStep(t *testing.T) {
	m := NewSetup()
	at, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // → provider
	// Navigate to Ollama (index 4).
	cur := at
	for i := 0; i < 4; i++ {
		cur, _ = cur.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	}
	sel, cmd := cur.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if sel.(SetupModel).Step() != StepComplete {
		t.Error("ollama should jump to complete (no key needed)")
	}
	if cmd == nil {
		t.Error("ollama selection should emit a done command")
	}
}

func TestSetup_APIKeyUsesEchoPassword(t *testing.T) {
	m := NewSetup()
	if !m.EchoPassword() {
		t.Error("API key input should use EchoPassword (masked)")
	}
}

func TestSetup_SkipGoesToComplete(t *testing.T) {
	m := NewSetup()
	at, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // → provider
	skip, cmd := at.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if skip.(SetupModel).Step() != StepComplete {
		t.Error("[s] should skip to complete")
	}
	if cmd == nil {
		t.Error("skip should emit a done command")
	}
}

func TestSetup_DoneMsgSkippedFlag(t *testing.T) {
	m := NewSetup()
	at, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, cmd := at.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	msg := cmd()
	done, ok := msg.(SetupDoneMsg)
	if !ok {
		t.Fatalf("expected SetupDoneMsg, got %T", msg)
	}
	if !done.Skipped {
		t.Error("skip should set Skipped=true")
	}
}

func TestSetup_FullFlowToComplete(t *testing.T) {
	m := NewSetup()
	s, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // welcome → provider
	s, _ = s.Update(tea.KeyMsg{Type: tea.KeyEnter})  // select Claude → API key
	sm := s.(SetupModel)
	sm.apiKey.SetValue("sk-ant-test")
	s2, _ := sm.Update(tea.KeyMsg{Type: tea.KeyEnter}) // key → telegram
	if s2.(SetupModel).Step() != StepTelegram {
		t.Fatalf("after key, step = %d, want telegram", s2.(SetupModel).Step())
	}
	s3, cmd := s2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}) // decline → creating
	if s3.(SetupModel).Step() != StepCreatingKey {
		t.Errorf("declining telegram should advance to creating-key")
	}
	if cmd == nil {
		t.Error("should emit done after telegram decision")
	}
}
