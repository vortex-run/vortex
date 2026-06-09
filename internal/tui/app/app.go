// Package app is the root Bubble Tea application for the VORTEX terminal UI.
// It composes the top bar, sidebar, per-screen view models (internal/tui/views),
// and a help bar. It imports both tui and views; neither imports app (no cycle).
package app

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vortex-run/vortex/internal/tui"
	"github.com/vortex-run/vortex/internal/tui/views"
)

// ViewID identifies a screen.
type ViewID int

// Views.
const (
	ViewOverview ViewID = iota
	ViewAgents
	ViewRoutes
	ViewNodes
	ViewLogs
	ViewMetrics
	ViewSecurity
	ViewSecrets
	ViewSetup
)

// sidebarWidth is the fixed left-nav width.
const sidebarWidth = 18

// SidebarItem is one navigation entry.
type SidebarItem struct {
	ID    ViewID
	Label string
}

// sidebarItems is the navigation order.
var sidebarItems = []SidebarItem{
	{ViewOverview, "Overview"},
	{ViewAgents, "Agents"},
	{ViewRoutes, "Routes"},
	{ViewNodes, "Nodes"},
	{ViewLogs, "Logs"},
	{ViewMetrics, "Metrics"},
	{ViewSecurity, "Security"},
	{ViewSecrets, "Secrets"},
}

// App is the root Bubble Tea model.
type App struct {
	client     *tui.Client
	activeView ViewID
	views      map[ViewID]tea.Model
	selected   int // sidebar selection index
	health     *tui.HealthData
	workingDir string          // shown in the top bar
	cost       *tui.AICostData // AI cost, shown in the top bar
	lastCost   time.Time       // last cost poll (every 30s)
	width      int
	height     int
	setupMode  bool
}

// tickMsg drives periodic data refresh.
type tickMsg time.Time

// NewApp constructs the app, choosing setup vs overview based on connectivity.
func NewApp(client *tui.Client) *App {
	a := &App{client: client, views: map[ViewID]tea.Model{}}

	a.views[ViewOverview] = views.NewOverview(client)
	a.views[ViewAgents] = views.NewAgents(client)
	a.views[ViewRoutes] = views.NewRoutes(client)
	a.views[ViewNodes] = views.NewNodes(client)
	a.views[ViewLogs] = views.NewLogs(client)
	a.views[ViewMetrics] = views.NewMetrics(client)
	a.views[ViewSecurity] = views.NewSecurity(client)
	a.views[ViewSecrets] = views.NewSecrets(client)
	a.views[ViewSetup] = views.NewSetup()

	if client != nil && client.IsConnected() {
		a.activeView = ViewOverview
	} else {
		a.activeView = ViewSetup
		a.setupMode = true
	}
	return a
}

// tick schedules the next refresh.
func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Init starts the active view and the refresh ticker.
func (a *App) Init() tea.Cmd {
	cmds := []tea.Cmd{tick()}
	if v, ok := a.views[a.activeView]; ok {
		cmds = append(cmds, v.Init())
	}
	return tea.Batch(cmds...)
}

// Update handles global keys, resize, ticks, and delegates to the active view.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		// Propagate a content-sized window to every view.
		inner := tea.WindowSizeMsg{Width: msg.Width - sidebarWidth - 2, Height: msg.Height - 4}
		for id, v := range a.views {
			a.views[id], _ = v.Update(inner)
		}
		return a, nil

	case tickMsg:
		// Refresh the active view's data, then schedule the next tick.
		var cmd tea.Cmd
		if v, ok := a.views[a.activeView]; ok {
			a.views[a.activeView], cmd = v.Update(views.RefreshMsg{})
		}
		// Keep the top bar's health + working dir fresh.
		if a.client != nil {
			if h, err := a.client.Health(); err == nil {
				a.health = h
			}
			if st, err := a.client.Status(); err == nil && st.WorkingDir != "" {
				a.workingDir = st.WorkingDir
			}
			// Poll AI cost at most every 30s.
			if time.Since(a.lastCost) >= 30*time.Second {
				if c, err := a.client.AICost(); err == nil {
					a.cost = c
				}
				a.lastCost = time.Now()
			}
		}
		return a, tea.Batch(cmd, tick())

	case views.SetupDoneMsg:
		// Setup finished → switch to overview (if we can connect).
		a.setupMode = false
		a.SwitchView(ViewOverview)
		return a, a.views[ViewOverview].Init()

	case tea.KeyMsg:
		// Global keys (skip while in setup so the wizard owns input).
		if !a.setupMode {
			switch msg.String() {
			case "q", "ctrl+c":
				return a, tea.Quit
			case "tab":
				a.cycleSidebar(1)
				return a, a.views[a.activeView].Init()
			case "shift+tab":
				a.cycleSidebar(-1)
				return a, a.views[a.activeView].Init()
			}
			// F1-F9 / 1-9 jump directly.
			if id, ok := directJump(msg.String()); ok {
				a.SwitchView(id)
				return a, a.views[a.activeView].Init()
			}
		} else if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
	}

	// Delegate to the active view.
	var cmd tea.Cmd
	if v, ok := a.views[a.activeView]; ok {
		a.views[a.activeView], cmd = v.Update(msg)
	}
	return a, cmd
}

// View composes the full layout: top bar, sidebar + content, help bar.
func (a *App) View() string {
	if a.setupMode {
		// The wizard takes the whole screen.
		return a.views[ViewSetup].View()
	}

	top := a.topBar()
	side := a.sidebar()
	content := ""
	if v, ok := a.views[a.activeView]; ok {
		content = v.View()
	}
	contentBox := lipgloss.NewStyle().Width(maxInt(a.width-sidebarWidth-2, 1)).Render(content)
	middle := lipgloss.JoinHorizontal(lipgloss.Top, side, "  ", contentBox)
	help := a.helpBar()
	return lipgloss.JoinVertical(lipgloss.Left, top, middle, help)
}

// topBar renders the title row.
// costPill renders the AI cost indicator: "free" for Ollama, else 💰 $X.XX
// colored green (<$1), amber ($1-5), or red (>$5).
func costPill(c *tui.AICostData) string {
	if c.Free {
		return tui.Pill("💰 free", tui.ColorSuccess)
	}
	color := tui.ColorSuccess
	switch {
	case c.TotalUSD > 5:
		color = tui.ColorDanger
	case c.TotalUSD >= 1:
		color = tui.ColorWarning
	}
	return tui.Pill(fmt.Sprintf("💰 $%.2f", c.TotalUSD), color)
}

func (a *App) topBar() string {
	parts := []string{tui.TitleStyle.Render("VORTEX")}
	if a.health != nil {
		parts = append(parts,
			tui.SubtitleStyle.Render(a.health.Version),
			tui.Pill(a.health.ClusterName, tui.ColorSuccess),
			tui.SubtitleStyle.Render(a.health.Uptime),
		)
	} else {
		parts = append(parts, tui.SubtitleStyle.Render("disconnected"))
	}
	if a.workingDir != "" {
		parts = append(parts, tui.Pill("📂 "+a.workingDir, tui.ColorPurple))
	}
	if a.cost != nil {
		parts = append(parts, costPill(a.cost))
	}
	parts = append(parts, tui.HelpStyle.Render("[Tab] views  [q] quit"))
	return strings.Join(parts, "  ")
}

// sidebar renders the left navigation.
func (a *App) sidebar() string {
	var b strings.Builder
	for i, item := range sidebarItems {
		line := item.Label
		if i == a.selected {
			b.WriteString(tui.SelectedStyle.Width(sidebarWidth).Render("▶ "+line) + "\n")
		} else {
			b.WriteString(tui.SubtitleStyle.Render("  "+line) + "\n")
		}
	}
	return lipgloss.NewStyle().Width(sidebarWidth).Render(b.String())
}

// helpBar renders context-sensitive hints for the active view.
func (a *App) helpBar() string {
	hints := map[ViewID]string{
		ViewOverview: "[r] Reload  [Tab] Navigate  [q] Quit",
		ViewAgents:   "[Enter] Send  [Tab] Complete  [Ctrl+L] Clear  [q] Quit",
		ViewRoutes:   "[j/k] Move  [Enter] Detail  [r] Reload  [q] Quit",
		ViewLogs:     "[f] Filter  [F] Follow  [c] Clear  [q] Quit",
		ViewSecrets:  "[s] Set  [j/k] Move  [q] Quit",
	}
	h := hints[a.activeView]
	if h == "" {
		h = "[Tab] Navigate  [q] Quit"
	}
	return tui.HelpStyle.Render(h)
}

// SwitchView changes the active view and syncs the sidebar selection.
func (a *App) SwitchView(v ViewID) {
	a.activeView = v
	for i, item := range sidebarItems {
		if item.ID == v {
			a.selected = i
			return
		}
	}
}

// cycleSidebar moves the selection by delta and switches to that view.
func (a *App) cycleSidebar(delta int) {
	n := len(sidebarItems)
	a.selected = (a.selected + delta + n) % n
	a.activeView = sidebarItems[a.selected].ID
}

// ActiveView exposes the current view (for tests).
func (a *App) ActiveView() ViewID { return a.activeView }

// SetupMode reports whether the app started in setup (for tests).
func (a *App) SetupMode() bool { return a.setupMode }

// directJump maps "1".."9" / "f1".."f9" to a ViewID.
func directJump(key string) (ViewID, bool) {
	order := []ViewID{
		ViewOverview, ViewAgents, ViewRoutes, ViewNodes,
		ViewLogs, ViewMetrics, ViewSecurity, ViewSecrets,
	}
	switch key {
	case "1", "f1":
		return order[0], true
	case "2", "f2":
		return order[1], true
	case "3", "f3":
		return order[2], true
	case "4", "f4":
		return order[3], true
	case "5", "f5":
		return order[4], true
	case "6", "f6":
		return order[5], true
	case "7", "f7":
		return order[6], true
	case "8", "f8":
		return order[7], true
	}
	return 0, false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
