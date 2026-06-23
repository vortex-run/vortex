package views

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseToolResult(t *testing.T) {
	tr := parseToolResult("write_file|calc.py|line1\nline2\nline3")
	if tr == nil {
		t.Fatal("expected a tool result")
	}
	if tr.Tool != "write_file" || tr.Target != "calc.py" {
		t.Errorf("tool/target = %q/%q", tr.Tool, tr.Target)
	}
	if tr.Lines != 3 {
		t.Errorf("lines = %d, want 3", tr.Lines)
	}
	if !tr.Collapsed {
		t.Error("tool result should default to collapsed")
	}
}

func TestParseToolResult_Malformed(t *testing.T) {
	if parseToolResult("garbage-no-pipe") != nil {
		t.Error("malformed tool_result content should parse to nil")
	}
}

func TestToolResult_HeaderAndExpand(t *testing.T) {
	tr := &ToolResult{Tool: "write_file", Target: "calc.py", Output: "a\nb\nc", Lines: 3, Collapsed: true}
	if !strings.Contains(tr.render(60), "▶") || strings.Contains(tr.render(60), "  1 a") {
		t.Errorf("collapsed render wrong:\n%s", tr.render(60))
	}
	tr.Collapsed = false
	out := tr.render(60)
	if !strings.Contains(out, "▼") || !strings.Contains(out, "a") || !strings.Contains(out, "  1") {
		t.Errorf("expanded render should show line-numbered body:\n%s", out)
	}
}

func TestCode_ToolResultCollapsedThenExpand(t *testing.T) {
	m := sizedCode(WithTeam())
	m, _ = m.HandleAGUI(CommsMsg{Time: time.Now(), From: "code-agent", To: "user", Kind: "tool_result", Content: "write_file|calc.py|def add(a,b):\n    return a+b"})
	if len(m.toolResults) != 1 {
		t.Fatalf("toolResults = %d, want 1", len(m.toolResults))
	}
	// Collapsed by default: header shows, body hidden.
	out := m.renderChat()
	if !strings.Contains(out, "▶") || !strings.Contains(out, "calc.py") {
		t.Errorf("collapsed tool row missing:\n%s", out)
	}
	if strings.Contains(out, "return a+b") {
		t.Errorf("collapsed row should not show body:\n%s", out)
	}
	// Esc to blur input, then Enter toggles expansion.
	m = blurred(m)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(CodeModel)
	if m.toolResults[0].Collapsed {
		t.Fatal("Enter should expand the focused tool row")
	}
	out = m.renderChat()
	if !strings.Contains(out, "▼") || !strings.Contains(out, "return a+b") {
		t.Errorf("expanded row should show code body:\n%s", out)
	}
}

func TestCode_ToolResultNavigation(t *testing.T) {
	m := blurred(sizedCode(WithTeam()))
	for _, c := range []string{"write_file|a.py|x", "write_file|b.py|y", "run_terminal|pytest|ok"} {
		m, _ = m.HandleAGUI(CommsMsg{Time: time.Now(), Kind: "tool_result", Content: c})
	}
	// Cursor focuses the newest (index 2).
	if m.toolCursor != 2 {
		t.Fatalf("toolCursor = %d, want 2 (newest)", m.toolCursor)
	}
	// ↑ moves up the list.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(CodeModel)
	if m.toolCursor != 1 {
		t.Errorf("after ↑, cursor = %d, want 1", m.toolCursor)
	}
	// Enter expands the focused (index 1) row only.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(CodeModel)
	if m.toolResults[1].Collapsed || !m.toolResults[0].Collapsed {
		t.Error("Enter should toggle only the focused row")
	}
}
