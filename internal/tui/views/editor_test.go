package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func key(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestEditor_StartsInInsertMode(t *testing.T) {
	e := NewEditor(true)
	if e.Mode() != ModeInsert {
		t.Errorf("vim editor should start in INSERT, got %s", e.Mode())
	}
}

func TestEditor_StandardModeAlwaysInsert(t *testing.T) {
	e := NewEditor(false)
	if e.Mode() != ModeInsert {
		t.Errorf("standard editor Mode = %s, want INSERT", e.Mode())
	}
	// Esc must not change anything in standard mode (it's a plain textarea).
	e, _ = e.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if e.Mode() != ModeInsert {
		t.Errorf("standard editor should stay INSERT after Esc, got %s", e.Mode())
	}
}

func TestEditor_EscSwitchesToNormal(t *testing.T) {
	e := NewEditor(true)
	e, _ = e.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if e.Mode() != ModeNormal {
		t.Errorf("Esc should switch to NORMAL, got %s", e.Mode())
	}
}

func TestEditor_iSwitchesToInsert(t *testing.T) {
	e := NewEditor(true)
	e, _ = e.Update(tea.KeyMsg{Type: tea.KeyEsc}) // → NORMAL
	e, _ = e.Update(key("i"))                     // → INSERT
	if e.Mode() != ModeInsert {
		t.Errorf("'i' should switch to INSERT, got %s", e.Mode())
	}
}

func TestEditor_vSwitchesToVisual(t *testing.T) {
	e := NewEditor(true)
	e, _ = e.Update(tea.KeyMsg{Type: tea.KeyEsc})
	e, _ = e.Update(key("v"))
	if e.Mode() != ModeVisual {
		t.Errorf("'v' should switch to VISUAL, got %s", e.Mode())
	}
}

func TestEditor_ddDeletesLine(t *testing.T) {
	e := NewEditor(true)
	e.SetValue("line one\nline two\nline three")
	e, _ = e.Update(tea.KeyMsg{Type: tea.KeyEsc}) // NORMAL
	// Cursor is at the last line after SetValue; go to the top first.
	e, _ = e.Update(key("g"))
	e, _ = e.Update(key("g"))
	e, _ = e.Update(key("d"))
	e, _ = e.Update(key("d"))
	if strings.Contains(e.Value(), "line one") {
		t.Errorf("dd should delete the current line; value = %q", e.Value())
	}
	if !strings.Contains(e.Value(), "line two") {
		t.Errorf("dd should leave other lines; value = %q", e.Value())
	}
}

func TestEditor_yyAndPaste(t *testing.T) {
	e := NewEditor(true)
	e.SetValue("alpha\nbeta")
	e, _ = e.Update(tea.KeyMsg{Type: tea.KeyEsc})
	e, _ = e.Update(key("g"))
	e, _ = e.Update(key("g"))
	e, _ = e.Update(key("y"))
	e, _ = e.Update(key("y"))
	e, _ = e.Update(key("p"))
	// "alpha" should now appear twice.
	if strings.Count(e.Value(), "alpha") != 2 {
		t.Errorf("yy + p should duplicate the line; value = %q", e.Value())
	}
}

func TestEditor_ModeIndicator(t *testing.T) {
	e := NewEditor(true)
	if !strings.Contains(e.ModeIndicator(), "INSERT") {
		t.Errorf("indicator = %q, want INSERT", e.ModeIndicator())
	}
	e, _ = e.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !strings.Contains(e.ModeIndicator(), "NORMAL") {
		t.Errorf("indicator = %q, want NORMAL", e.ModeIndicator())
	}
}

func TestEditor_ColonWTriggersSubmit(t *testing.T) {
	e := NewEditor(true)
	if !e.SubmitOnColonW(":w") {
		t.Error(":w should be recognised")
	}
	if !e.Submitted() {
		t.Error(":w should set Submitted")
	}
	if e.Submitted() {
		t.Error("Submitted should clear after reading")
	}

	if !e.SubmitOnColonW(":q") || !e.Quit() {
		t.Error(":q should set Quit")
	}
	if e.SubmitOnColonW(":bogus") {
		t.Error(":bogus should not be recognised")
	}
}

func TestEditor_StandardModeUnaffectedByVimKeys(t *testing.T) {
	e := NewEditor(false)
	e, _ = e.Update(key("d"))
	e, _ = e.Update(key("d"))
	// In standard mode, "dd" is literal text, not a delete operator.
	if e.Value() != "dd" {
		t.Errorf("standard mode should type literally; value = %q, want dd", e.Value())
	}
}

func TestEditor_VisualEscReturnsNormal(t *testing.T) {
	e := NewEditor(true)
	e, _ = e.Update(tea.KeyMsg{Type: tea.KeyEsc}) // NORMAL
	e, _ = e.Update(key("v"))                     // VISUAL
	e, _ = e.Update(tea.KeyMsg{Type: tea.KeyEsc}) // back to NORMAL
	if e.Mode() != ModeNormal {
		t.Errorf("Esc from VISUAL should return to NORMAL, got %s", e.Mode())
	}
}
