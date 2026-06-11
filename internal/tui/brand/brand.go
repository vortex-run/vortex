// Package brand is the single source of truth for VORTEX's visual identity:
// logo, tagline, color palette, icons, risk levels, and the Lip Gloss styles
// built from them. Every TUI surface (and the CLI banners) renders through
// these constants, so one change here restyles the whole product.
package brand

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Logo is the full ASCII wordmark shown on setup and splash screens.
const Logo = `
 ██╗   ██╗ ██████╗ ██████╗ ████████╗███████╗██╗  ██╗
 ██║   ██║██╔═══██╗██╔══██╗╚══██╔══╝██╔════╝╚██╗██╔╝
 ██║   ██║██║   ██║██████╔╝   ██║   █████╗   ╚███╔╝
 ╚██╗ ██╔╝██║   ██║██╔══██╗   ██║   ██╔══╝   ██╔██╗
  ╚████╔╝ ╚██████╔╝██║  ██║   ██║   ███████╗██╔╝ ██╗
   ╚═══╝   ╚═════╝ ╚═╝  ╚═╝   ╚═╝   ╚══════╝╚═╝  ╚═╝`

// LogoSmall is the compact mark for top bars and one-line banners.
const LogoSmall = `▲ VORTEX`

// Tagline is the product one-liner shown beneath the logo.
const Tagline = "One binary. Any server. Fully autonomous."

// Version is the brand display version.
const Version = "v1.0.0"

// Color palette — used everywhere.
const (
	ColorPrimary    = "#378ADD"
	ColorSuccess    = "#1D9E75"
	ColorWarning    = "#EF9F27"
	ColorDanger     = "#D85A30"
	ColorPurple     = "#534AB7"
	ColorBackground = "#0D0D0D"
	ColorSurface    = "#141414"
	ColorBorder     = "#2A2A2A"
	ColorText       = "#E8E8E8"
	ColorTextDim    = "#666666"
	ColorSelected   = "#1A2A3A"
)

// Icons — consistent everywhere.
const (
	IconSuccess = "✓"
	IconError   = "✗"
	IconWarn    = "⚠"
	IconInfo    = "→"
	IconSpinner = "⠸"
	IconAgent   = "◆"
	IconUser    = "▸"
	IconFile    = "📄"
	IconFolder  = "📁"
	IconRocket  = "🚀"
	IconKey     = "🔑"
	IconCost    = "💰"
	IconShield  = "🛡"
	IconPulse   = "●"
	IconIdle    = "○"
	IconBusy    = "◉"
	IconArrow   = "›"
	IconSep     = "─"
	IconVSep    = "│"
)

// Risk levels for the approval UI.
const (
	RiskLow      = "LOW RISK"
	RiskMedium   = "MEDIUM RISK"
	RiskHigh     = "HIGH RISK"
	RiskCritical = "CRITICAL — review carefully"
)

// RiskColor returns the palette color for a risk level.
func RiskColor(risk string) string {
	switch risk {
	case RiskLow:
		return ColorSuccess
	case RiskMedium:
		return ColorWarning
	default: // high and critical both render in danger red
		return ColorDanger
	}
}

// Lip Gloss styles built from the brand constants.
var (
	StyleTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ColorPrimary))
	StyleSubtitle = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorTextDim))

	StyleBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(ColorBorder))

	StyleSelected = lipgloss.NewStyle().
			Background(lipgloss.Color(ColorSelected)).
			Foreground(lipgloss.Color(ColorText))

	StyleActive = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(ColorPrimary))

	StyleInactive = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(ColorTextDim))

	StyleSuccess = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSuccess)).Bold(true)
	StyleWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorWarning)).Bold(true)
	StyleError   = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorDanger)).Bold(true)

	StyleCode = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorText)).
			Background(lipgloss.Color(ColorSurface))

	StyleUserMsg = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorPrimary)).
			Align(lipgloss.Right)

	StyleAgentMsg = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorText)).
			Background(lipgloss.Color(ColorSurface)).
			Align(lipgloss.Left)

	StyleSystemMsg = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorTextDim)).
			Align(lipgloss.Center)

	StyleTopBar = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorText)).
			Background(lipgloss.Color(ColorSurface)).
			Padding(0, 1)

	StyleSidebar = lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColorTextDim)).
			Padding(0, 1)

	StyleHelp = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorTextDim))

	StyleApproval = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(ColorWarning)).
			Padding(0, 1)

	StyleProgress = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorPrimary))

	StyleCard = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(ColorBorder)).
			Padding(0, 1)

	StyleTableHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ColorPrimary))
	StyleTableRow    = lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	StyleTableAlt    = lipgloss.NewStyle().
				Foreground(lipgloss.Color(ColorText)).
				Background(lipgloss.Color(ColorSurface))
)

// Pill renders a small colored pill: "● text" in the given hex color.
func Pill(text, color string) string {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(color)).
		Render(IconPulse + " " + text)
}

// StatusDot returns a "●" colored green when healthy, red otherwise.
func StatusDot(healthy bool) string {
	color := ColorDanger
	if healthy {
		color = ColorSuccess
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(IconPulse)
}

// MaskSecret returns the first 4 characters followed by "****" so a secret can
// be identified without being revealed. Strings of 4 or fewer characters are
// returned unchanged (nothing useful to hide behind a mask that short).
func MaskSecret(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[:4] + "****"
}

// FormatCost renders a USD amount as "$0.023", or "free" for zero.
func FormatCost(usd float64) string {
	if usd == 0 {
		return "free"
	}
	return fmt.Sprintf("$%.3f", usd)
}

// FormatDuration renders a duration compactly: "2h 14m", "14m 5s", "45s".
func FormatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// ProgressBar renders a "████████░░░░" bar of exactly width cells for a
// percentage in [0,100]: filled cells in the primary color, the rest dim.
func ProgressBar(percent float64, width int) string {
	if width <= 0 {
		return ""
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := int(percent/100*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	bar := StyleProgress.Render(strings.Repeat("█", filled))
	bar += lipgloss.NewStyle().
		Foreground(lipgloss.Color(ColorTextDim)).
		Render(strings.Repeat("░", width-filled))
	return bar
}

// RiskBadge renders a colored "[LOW RISK]"-style badge for a risk level.
func RiskBadge(risk string) string {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(RiskColor(risk))).
		Bold(true).
		Render("[" + risk + "]")
}
