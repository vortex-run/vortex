package views

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vortex-run/vortex/internal/tui/brand"
)

// HelpViewID identifies which view's help content to show. It mirrors the
// app package's ViewID order but lives here so views does not import app
// (no cycle).
type HelpViewID int

// Help view identifiers (kept in sidebar order).
const (
	HelpOverview HelpViewID = iota
	HelpAgents
	HelpCode
	HelpRoutes
	HelpNodes
	HelpLogs
	HelpMetrics
	HelpSecurity
	HelpSecrets
)

// HelpItem is one keyboard shortcut or command with an optional example.
type HelpItem struct {
	Key     string
	Action  string
	Example string
}

// HelpSection groups related help items under a heading.
type HelpSection struct {
	Title string
	Items []HelpItem
}

// HelpContent is a view's complete help, shown in the overlay.
type HelpContent struct {
	Title    string
	Sections []HelpSection
}

// GetHelp returns the help content for a view. Unknown views get the generic
// navigation help so the overlay is never empty.
func GetHelp(id HelpViewID) HelpContent {
	switch id {
	case HelpAgents:
		return agentsHelp
	case HelpOverview:
		return overviewHelp
	case HelpLogs:
		return logsHelp
	case HelpCode:
		return codeHelp
	default:
		return genericHelp
	}
}

var agentsHelp = HelpContent{
	Title: "Agent Chat",
	Sections: []HelpSection{
		{Title: "Keyboard shortcuts", Items: []HelpItem{
			{Key: "Enter", Action: "Send message"},
			{Key: "↑/↓", Action: "Message history"},
			{Key: "Tab", Action: "Autocomplete command"},
			{Key: "Ctrl+L", Action: "Clear chat"},
			{Key: "Y+Enter", Action: "Approve action"},
			{Key: "N+Enter", Action: "Reject action"},
		}},
		{Title: "Slash commands", Items: []HelpItem{
			{Key: "/ls [path]", Action: "List files"},
			{Key: "/read <file>", Action: "Read a file"},
			{Key: "/run <cmd>", Action: "Run a command"},
			{Key: "/search <q>", Action: "Search files"},
			{Key: "/git", Action: "Git status"},
			{Key: "/undo", Action: "Undo last file change"},
			{Key: "/history", Action: "Show past sessions"},
			{Key: "/help", Action: "Show this help"},
		}},
		{Title: "Example tasks", Items: []HelpItem{
			{Example: "create a python web scraper"},
			{Example: "add auth to my FastAPI app"},
			{Example: "research golang frameworks"},
			{Example: "fix the bug in main.py"},
			{Example: "deploy to my VPS"},
		}},
	},
}

var overviewHelp = HelpContent{
	Title: "System Overview",
	Sections: []HelpSection{
		{Title: "Status indicators", Items: []HelpItem{
			{Key: brand.IconPulse + " green", Action: "healthy and running"},
			{Key: brand.IconPulse + " amber", Action: "degraded or warning"},
			{Key: brand.IconPulse + " red", Action: "down or error"},
		}},
		{Title: "Keyboard shortcuts", Items: []HelpItem{
			{Key: "r", Action: "Reload configuration"},
			{Key: "Tab", Action: "Navigate to next view"},
		}},
	},
}

var logsHelp = HelpContent{
	Title: "Log Viewer",
	Sections: []HelpSection{
		{Title: "Keyboard shortcuts", Items: []HelpItem{
			{Key: "f", Action: "Focus filter input"},
			{Key: "F", Action: "Toggle follow mode (auto-scroll)"},
			{Key: "c", Action: "Clear displayed logs"},
			{Key: "/", Action: "Search in logs"},
		}},
		{Title: "Log levels", Items: []HelpItem{
			{Key: "INFO", Action: "normal operation"},
			{Key: "WARN", Action: "check this"},
			{Key: "ERROR", Action: "something failed"},
		}},
	},
}

var codeHelp = HelpContent{
	Title: "VORTEX Code",
	Sections: []HelpSection{
		{Title: "Keyboard shortcuts", Items: []HelpItem{
			{Key: "P", Action: "Pause/resume agents"},
			{Key: "S", Action: "Stop task"},
			{Key: "T", Action: "Forward to Telegram"},
			{Key: "?", Action: "This help"},
		}},
		{Title: "How it works", Items: []HelpItem{
			{Action: "1. Type your coding task"},
			{Action: "2. Coordinator plans the work"},
			{Action: "3. Specialist agents execute"},
			{Action: "4. You see every step live"},
			{Action: "5. Approve the final result"},
		}},
		{Title: "Example tasks", Items: []HelpItem{
			{Example: "build a REST API with auth"},
			{Example: "add tests to all my files"},
			{Example: "refactor to use async/await"},
			{Example: "create a Flutter calculator"},
		}},
	},
}

var genericHelp = HelpContent{
	Title: "Navigation",
	Sections: []HelpSection{
		{Title: "Keyboard shortcuts", Items: []HelpItem{
			{Key: "Tab", Action: "Next view"},
			{Key: "Shift+Tab", Action: "Previous view"},
			{Key: "1-9", Action: "Jump to view"},
			{Key: "?", Action: "Toggle this help"},
			{Key: "q", Action: "Quit"},
		}},
	},
}

// HelpModel is a full-screen, scrollable help overlay for a view.
type HelpModel struct {
	content HelpContent
	closed  bool
	width   int
	height  int
}

// NewHelp constructs a help overlay for the given view.
func NewHelp(id HelpViewID) HelpModel {
	return HelpModel{content: GetHelp(id)}
}

// Init satisfies tea.Model (no startup command).
func (m HelpModel) Init() tea.Cmd { return nil }

// Update closes the overlay on ? or Esc and tracks resizing.
func (m HelpModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "?", "esc", "q":
			m.closed = true
		}
	}
	return m, nil
}

// Closed reports whether the overlay has been dismissed.
func (m HelpModel) Closed() bool { return m.closed }

// Content exposes the help content (for tests).
func (m HelpModel) Content() HelpContent { return m.content }

// View renders the help overlay.
func (m HelpModel) View() string {
	return m.render(m.width, m.height)
}

// render builds the bordered, centered overlay at the given size.
func (m HelpModel) render(w, h int) string {
	var b strings.Builder
	b.WriteString(brand.StyleTitle.Render("Help — "+m.content.Title) + "\n\n")
	for _, sec := range m.content.Sections {
		b.WriteString(brand.StyleTitle.Render(sec.Title) + "\n")
		for _, it := range sec.Items {
			switch {
			case it.Example != "":
				b.WriteString("  " + brand.StyleSubtitle.Render(`"`+it.Example+`"`) + "\n")
			case it.Key != "":
				b.WriteString("  " + padKey(it.Key) + " " + it.Action + "\n")
			default:
				b.WriteString("  " + it.Action + "\n")
			}
		}
		b.WriteString("\n")
	}
	b.WriteString(brand.StyleSubtitle.Render("Press ? or Esc to close"))

	box := brand.StyleActive.Padding(1, 2).Render(b.String())
	if w < 20 || h < 8 {
		return box
	}
	return lipgloss.Place(w, maxIntH(h-1, 10), lipgloss.Center, lipgloss.Center, box)
}

// padKey right-pads a key column to a consistent width.
func padKey(k string) string {
	const w = 13
	if lipgloss.Width(k) >= w {
		return k
	}
	return k + strings.Repeat(" ", w-lipgloss.Width(k))
}

// maxIntH returns the larger of a and b.
func maxIntH(a, b int) int {
	if a > b {
		return a
	}
	return b
}
