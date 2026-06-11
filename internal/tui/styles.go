// Package tui implements VORTEX's full-screen terminal UI (a Bubble Tea
// application). This file re-exports the shared palette, styles, and render
// helpers from the brand package (the single source of truth for VORTEX's
// visual identity) under the names the views were built against. Note: unlike
// the rest of VORTEX (stdlib-only), the TUI depends on the Charm ecosystem
// (bubbletea/lipgloss/bubbles) — an approved, deliberate departure.
package tui

import (
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"

	"github.com/vortex-run/vortex/internal/tui/brand"
)

// Color palette — aliases of the brand palette. Change brand, not these.
const (
	ColorPrimary    = brand.ColorPrimary
	ColorSuccess    = brand.ColorSuccess
	ColorWarning    = brand.ColorWarning
	ColorDanger     = brand.ColorDanger
	ColorPurple     = brand.ColorPurple
	ColorText       = brand.ColorText
	ColorTextDim    = brand.ColorTextDim
	ColorBorder     = brand.ColorBorder
	ColorBackground = brand.ColorBackground
	ColorSelected   = brand.ColorSelected
)

// Shared styles — aliases of the brand styles so every view stays consistent.
var (
	TitleStyle    = brand.StyleTitle
	SubtitleStyle = brand.StyleSubtitle

	BorderStyle   = brand.StyleBorder
	SelectedStyle = brand.StyleSelected
	ActiveStyle   = brand.StyleActive
	InactiveStyle = brand.StyleInactive

	StatusOKStyle    = brand.StyleSuccess
	StatusWarnStyle  = brand.StyleWarn
	StatusErrorStyle = brand.StyleError

	CodeStyle = brand.StyleCode

	TableHeaderStyle = brand.StyleTableHeader
	TableRowStyle    = brand.StyleTableRow
	TableAltRowStyle = brand.StyleTableAlt

	HelpStyle = brand.StyleHelp

	InputStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(brand.ColorPrimary)).
			Padding(0, 1)

	ChatUserStyle   = brand.StyleUserMsg
	ChatAgentStyle  = brand.StyleAgentMsg
	ChatSystemStyle = brand.StyleSystemMsg
)

// Pill renders a small colored pill: "● text" in the given hex color.
func Pill(text, color string) string { return brand.Pill(text, color) }

// StatusDot returns a "●" colored green when ok, red otherwise.
func StatusDot(ok bool) string { return brand.StatusDot(ok) }

// Spinner returns a configured Bubble Tea spinner (dot style, primary color).
func Spinner() spinner.Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = brand.StyleProgress
	return s
}
