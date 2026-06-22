package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseOptions_ValidMenu(t *testing.T) {
	resp := "QUESTION: What framework should I use?\nOPTIONS:\n- Flask (simple, lightweight)\n- Django (full-featured)\n- FastAPI (modern, async)"
	sel := parseOptions(resp)
	if sel == nil {
		t.Fatal("expected a selector")
	}
	if sel.Question != "What framework should I use?" {
		t.Errorf("question = %q", sel.Question)
	}
	if len(sel.Options) != 3 || sel.Options[0] != "Flask (simple, lightweight)" || sel.Options[2] != "FastAPI (modern, async)" {
		t.Errorf("options = %+v", sel.Options)
	}
	if !sel.Active {
		t.Error("selector should be active")
	}
}

func TestParseOptions_NotAMenu(t *testing.T) {
	for _, resp := range []string{
		"I'll build a Flask calculator now.",
		"QUESTION: missing options?", // no OPTIONS:
		"OPTIONS:\n- a\n- b",         // no QUESTION:
	} {
		if sel := parseOptions(resp); sel != nil {
			t.Errorf("parseOptions(%q) should be nil, got %+v", resp, sel)
		}
	}
}

func TestOptionSelector_CursorWraps(t *testing.T) {
	s := &OptionSelector{Options: []string{"a", "b", "c"}}
	s.moveCursor(-1) // wrap to bottom
	if s.Cursor != 2 {
		t.Errorf("up from 0 = %d, want 2", s.Cursor)
	}
	s.moveCursor(1) // wrap to top
	if s.Cursor != 0 {
		t.Errorf("down from 2 = %d, want 0", s.Cursor)
	}
	if s.Selected() != "a" {
		t.Errorf("selected = %q, want a", s.Selected())
	}
}

func TestCode_SelectorFromCoordinatorReply(t *testing.T) {
	m := sizedCode(WithTeam())
	m.working = true
	resp := "QUESTION: Which framework?\nOPTIONS:\n- Flask\n- Django"
	updated, _ := m.Update(codeReplyMsg{content: resp})
	m = updated.(CodeModel)
	if m.selector == nil || !m.selector.Active {
		t.Fatal("coordinator menu reply should activate the selector")
	}
	out := m.renderChat()
	if !strings.Contains(out, "Which framework?") || !strings.Contains(out, "❯ Flask") {
		t.Errorf("selector not rendered:\n%s", out)
	}
	// Raw QUESTION:/OPTIONS: text must not appear as a plain chat line.
	if strings.Contains(out, "OPTIONS:") {
		t.Errorf("raw OPTIONS marker leaked into chat:\n%s", out)
	}
}

func TestCode_SelectorArrowAndEnter(t *testing.T) {
	m := sizedCode(WithTeam())
	m.selector = &OptionSelector{Question: "Which?", Options: []string{"Flask", "Django"}, Active: true}
	m.input.Blur() // selector owns keys regardless of input focus

	// ↓ moves to Django.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(CodeModel)
	if m.selector.Cursor != 1 {
		t.Fatalf("cursor = %d after ↓, want 1", m.selector.Cursor)
	}
	// Enter submits the highlighted option as a user message + starts a task.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(CodeModel)
	if m.selector != nil {
		t.Error("selector should clear after Enter")
	}
	last := m.Chat()[len(m.Chat())-1]
	if last.Role != "user" || last.Content != "Django" {
		t.Errorf("submitted line = %+v, want user 'Django'", last)
	}
	if !m.Working() || cmd == nil {
		t.Error("Enter should submit the choice and start working")
	}
}

func TestCode_SelectorEscDismisses(t *testing.T) {
	m := sizedCode(WithTeam())
	m.selector = &OptionSelector{Question: "Which?", Options: []string{"a", "b"}, Active: true}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(CodeModel)
	if m.selector != nil {
		t.Error("Esc should dismiss the selector for free-text input")
	}
}
