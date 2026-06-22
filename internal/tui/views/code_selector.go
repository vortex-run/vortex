package views

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/vortex-run/vortex/internal/tui/brand"
)

// OptionSelector is an interactive arrow-key menu rendered in the CHAT panel
// when the coordinator asks a multiple-choice question (QUESTION:/OPTIONS:
// format). Enter submits the highlighted option as the next message.
type OptionSelector struct {
	Question string
	Options  []string
	Cursor   int
	Active   bool
}

// parseOptions extracts an OptionSelector from a coordinator reply that uses the
// QUESTION:/OPTIONS: format. It returns nil when the reply is not a menu.
//
//	QUESTION: What framework should I use?
//	OPTIONS:
//	- Flask (simple, lightweight)
//	- Django (full-featured)
func parseOptions(response string) *OptionSelector {
	if !strings.Contains(response, "QUESTION:") || !strings.Contains(response, "OPTIONS:") {
		return nil
	}
	var question string
	var options []string
	inOptions := false
	for _, line := range strings.Split(response, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "QUESTION:"):
			question = strings.TrimSpace(strings.TrimPrefix(trimmed, "QUESTION:"))
			inOptions = false
		case strings.HasPrefix(trimmed, "OPTIONS:"):
			inOptions = true
		case inOptions && strings.HasPrefix(trimmed, "- "):
			if opt := strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")); opt != "" {
				options = append(options, opt)
			}
		}
	}
	if question == "" || len(options) == 0 {
		return nil
	}
	return &OptionSelector{Question: question, Options: options, Active: true}
}

// moveCursor advances the selector cursor by delta with wrap-around.
func (s *OptionSelector) moveCursor(delta int) {
	n := len(s.Options)
	if n == 0 {
		return
	}
	s.Cursor = ((s.Cursor+delta)%n + n) % n
}

// Selected returns the currently highlighted option text.
func (s *OptionSelector) Selected() string {
	if s.Cursor < 0 || s.Cursor >= len(s.Options) {
		return ""
	}
	return s.Options[s.Cursor]
}

// renderSelector renders the active option menu for the CHAT panel.
func renderSelector(s *OptionSelector, width int) string {
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorPrimary)).Render("? "+s.Question) + "\n")
	for i, opt := range s.Options {
		if i == s.Cursor {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorSuccess)).Bold(true).
				Render("❯ "+opt) + "\n")
		} else {
			b.WriteString(brand.StyleSubtitle.Render("  "+opt) + "\n")
		}
	}
	b.WriteString("\n" + brand.StyleHelp.Render("↑↓ move · Enter select · Esc for text"))
	return lipgloss.NewStyle().Width(maxInt2(width, 24)).Render(b.String())
}
