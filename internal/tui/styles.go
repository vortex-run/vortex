// Package tui implements VORTEX's full-screen terminal UI (a Bubble Tea
// application). This file defines the shared Lip Gloss color palette and styles
// used across every view, plus small render helpers (pills, status dots,
// spinner). Note: unlike the rest of VORTEX (stdlib-only), the TUI depends on
// the Charm ecosystem (bubbletea/lipgloss/bubbles) — an approved, deliberate
// departure for this feature.
package tui

import (
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
)

// Color palette — dark theme matching the web dashboard.
const (
	ColorPrimary    = "#378ADD" // blue
	ColorSuccess    = "#1D9E75" // green
	ColorWarning    = "#EF9F27" // amber
	ColorDanger     = "#D85A30" // red
	ColorPurple     = "#534AB7"
	ColorText       = "#E0E0E0"
	ColorTextDim    = "#8B8B8B"
	ColorBorder     = "#2D2D2D"
	ColorBackground = "#0D0D0D"
	ColorSelected   = "#1A2A3A"
)

// Shared styles. These are package-level so views share a consistent look.
var (
	TitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ColorPrimary))
	SubtitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorTextDim))

	BorderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(ColorBorder))

	SelectedStyle = lipgloss.NewStyle().
			Background(lipgloss.Color(ColorSelected)).
			Foreground(lipgloss.Color(ColorText))

	ActiveStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(ColorPrimary))

	InactiveStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(ColorTextDim))

	StatusOKStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorSuccess)).
			Bold(true)

	StatusWarnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorWarning)).
			Bold(true)

	StatusErrorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(ColorDanger)).
				Bold(true)

	CodeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorText)).
			Background(lipgloss.Color(ColorBorder))

	TableHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(ColorPrimary))

	TableRowStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorText))

	TableAltRowStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(ColorText)).
				Background(lipgloss.Color("#151515"))

	HelpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorTextDim))

	InputStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(ColorPrimary)).
			Padding(0, 1)

	ChatUserStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorPrimary)).
			Align(lipgloss.Right)

	ChatAgentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorSuccess)).
			Align(lipgloss.Left)

	ChatSystemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorTextDim)).
			Align(lipgloss.Center)
)

// Pill renders a small colored pill: "● text" in the given hex color.
func Pill(text, color string) string {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(color)).
		Render("● " + text)
}

// StatusDot returns a "●" colored green when ok, red otherwise.
func StatusDot(ok bool) string {
	color := ColorDanger
	if ok {
		color = ColorSuccess
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render("●")
}

// Spinner returns a configured Bubble Tea spinner (dot style, primary color).
func Spinner() spinner.Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorPrimary))
	return s
}
