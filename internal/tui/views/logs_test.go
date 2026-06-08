package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func sampleLines() []LogLine {
	return []LogLine{
		{Time: "19:11:49", Level: "INFO", Msg: "VORTEX started", Fields: map[string]string{"cluster": "c1"}},
		{Time: "19:11:50", Level: "INFO", Msg: "route started", Fields: map[string]string{"name": "api"}},
		{Time: "19:12:10", Level: "WARN", Msg: "gossip join failed"},
		{Time: "19:14:29", Level: "ERROR", Msg: "boom"},
	}
}

func TestLogs_SetAndRender(t *testing.T) {
	m := NewLogs(nil)
	m.SetLines(sampleLines())
	if m.LineCount() != 4 {
		t.Fatalf("LineCount = %d, want 4", m.LineCount())
	}
	out := m.renderLines()
	if !strings.Contains(out, "VORTEX started") || !strings.Contains(out, "gossip join failed") {
		t.Errorf("rendered logs missing content:\n%s", out)
	}
}

func TestLogs_Filter(t *testing.T) {
	m := NewLogs(nil)
	m.SetLines(sampleLines())
	m.filter.SetValue("gossip")
	got := m.filtered()
	if len(got) != 1 || got[0].Msg != "gossip join failed" {
		t.Errorf("filter 'gossip' = %+v, want 1 match", got)
	}
}

func TestLogs_FilterByField(t *testing.T) {
	m := NewLogs(nil)
	m.SetLines(sampleLines())
	m.filter.SetValue("name=api")
	got := m.filtered()
	if len(got) != 1 || got[0].Msg != "route started" {
		t.Errorf("field filter = %+v, want the route line", got)
	}
}

func TestLogs_FollowToggle(t *testing.T) {
	m := NewLogs(nil)
	if !m.Follow() {
		t.Fatal("follow should default true")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	if updated.(LogsModel).Follow() {
		t.Error("F should toggle follow off")
	}
}

func TestLogs_FilterFocus(t *testing.T) {
	m := NewLogs(nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if !updated.(LogsModel).Filtering() {
		t.Error("f should focus the filter input")
	}
}

func TestLogs_Clear(t *testing.T) {
	m := NewLogs(nil)
	m.SetLines(sampleLines())
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if updated.(LogsModel).LineCount() != 0 {
		t.Error("c should clear displayed lines")
	}
}

func TestLogs_EmptyState(t *testing.T) {
	m := NewLogs(nil)
	if !strings.Contains(m.renderLines(), "no log lines") {
		t.Errorf("empty logs should show a placeholder, got: %s", m.renderLines())
	}
}
