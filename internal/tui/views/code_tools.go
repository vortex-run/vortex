package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/vortex-run/vortex/internal/tui/brand"
)

// toolResultExpandLines is how many lines an expanded tool result shows.
const toolResultExpandLines = 20

// ToolResult is one agent tool call (write_file/run_terminal) rendered in the
// chat panel as a collapsible Claude-Code-style row.
type ToolResult struct {
	Tool      string // "write_file" | "edit_file" | "run_terminal"
	Target    string // filename or command
	Output    string // full content / command output
	Lines     int
	Collapsed bool // default true
}

// parseToolResult decodes a "tool_result" bus message Content of the form
// "tool|target|output" into a ToolResult (collapsed by default). Returns nil
// when the content is malformed.
func parseToolResult(content string) *ToolResult {
	parts := strings.SplitN(content, "|", 3)
	if len(parts) < 2 {
		return nil
	}
	tr := &ToolResult{Tool: parts[0], Target: parts[1], Collapsed: true}
	if len(parts) == 3 {
		tr.Output = parts[2]
		tr.Lines = countOutputLines(parts[2])
	}
	return tr
}

// countOutputLines counts the lines in s (1 for a single non-empty line).
func countOutputLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// header renders the collapsed/expanded header row, e.g.
// "▶ write_file  calc.py  (89 lines)".
func (tr *ToolResult) header() string {
	icon := "▶"
	if !tr.Collapsed {
		icon = "▼"
	}
	detail := ""
	switch tr.Tool {
	case "run_terminal":
		detail = ""
	default:
		if tr.Lines > 0 {
			detail = fmt.Sprintf("  (%d lines)", tr.Lines)
		}
	}
	toolStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorSuccess))
	return brand.StyleSubtitle.Render(icon) + " " +
		toolStyle.Render(tr.Tool) + "  " + tr.Target +
		brand.StyleSubtitle.Render(detail)
}

// render renders the tool row: header, plus a line-numbered body when expanded.
func (tr *ToolResult) render(width int) string {
	var b strings.Builder
	b.WriteString(tr.header() + "\n")
	if tr.Collapsed || tr.Output == "" {
		return b.String()
	}
	lines := strings.Split(tr.Output, "\n")
	shown := lines
	truncated := false
	if len(shown) > toolResultExpandLines {
		shown = shown[:toolResultExpandLines]
		truncated = true
	}
	var body strings.Builder
	for i, line := range shown {
		fmt.Fprintf(&body, "%s %s\n",
			brand.StyleSubtitle.Render(fmt.Sprintf("%3d", i+1)), line)
	}
	if truncated {
		body.WriteString(brand.StyleSubtitle.Render(fmt.Sprintf("  … %d more lines", len(lines)-toolResultExpandLines)))
	}
	b.WriteString(brand.StyleBorder.Padding(0, 1).Width(maxInt2(width-2, 20)).Render(strings.TrimRight(body.String(), "\n")) + "\n")
	return b.String()
}
