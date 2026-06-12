// Package app is the root Bubble Tea application for the VORTEX terminal UI.
// It composes the top bar, sidebar, per-screen view models (internal/tui/views),
// a context help bar, a ?-toggled help overlay, and a first-run tutorial. It
// imports both tui and views; neither imports app (no cycle).
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vortex-run/vortex/internal/tui"
	"github.com/vortex-run/vortex/internal/tui/brand"
	"github.com/vortex-run/vortex/internal/tui/views"
)

// ViewID identifies a screen.
type ViewID int

// Views.
const (
	ViewOverview ViewID = iota
	ViewAgents
	ViewCode
	ViewRoutes
	ViewNodes
	ViewLogs
	ViewMetrics
	ViewSecurity
	ViewSecrets
	ViewSetup
)

// sidebarWidth is the fixed left-nav width.
const sidebarWidth = 20

// SidebarItem is one navigation entry.
type SidebarItem struct {
	ID    ViewID
	Label string
}

// sidebarItems is the navigation order.
var sidebarItems = []SidebarItem{
	{ViewOverview, "Overview"},
	{ViewAgents, "Agents"},
	{ViewCode, "Code"},
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

	helpOpen     bool // ?-toggled help overlay
	tutorialStep int  // -1 = off, otherwise index into tutorialSteps
}

// InputFocused is implemented by views that own a text input. When the active
// view reports its input is focused, the app stops handling navigation keys
// (q/Tab/1-9/F1-F9) so the user can type those characters into the input.
type InputFocused interface {
	IsInputFocused() bool
}

// activeViewInputFocused reports whether the active view has a focused input.
func (a *App) activeViewInputFocused() bool {
	if v, ok := a.views[a.activeView].(InputFocused); ok {
		return v.IsInputFocused()
	}
	return false
}

// tickMsg drives periodic data refresh.
type tickMsg time.Time

// NewApp constructs the app, choosing setup vs overview based on connectivity.
func NewApp(client *tui.Client) *App {
	a := &App{client: client, views: map[ViewID]tea.Model{}, tutorialStep: -1}

	a.views[ViewOverview] = views.NewOverview(client)
	a.views[ViewAgents] = views.NewAgents(client)
	a.views[ViewCode] = views.NewCode(client)
	a.views[ViewRoutes] = views.NewRoutes(client)
	a.views[ViewNodes] = views.NewNodes(client)
	a.views[ViewLogs] = views.NewLogs(client)
	a.views[ViewMetrics] = views.NewMetrics(client)
	a.views[ViewSecurity] = views.NewSecurity(client)
	a.views[ViewSecrets] = views.NewSecrets(client)
	a.views[ViewSetup] = views.NewSetup()

	if client != nil && client.IsConnected() {
		a.activeView = ViewOverview
		a.startTutorialIfFirstRun()
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
		// Ctrl+C always quits, even while typing.
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
		// The first-run tutorial owns the keyboard while visible.
		if a.tutorialStep >= 0 {
			switch msg.String() {
			case "right", "enter", " ":
				a.tutorialStep++
				if a.tutorialStep >= len(tutorialSteps) {
					a.finishTutorial()
				}
			case "esc":
				a.finishTutorial()
			}
			return a, nil
		}
		// The help overlay swallows keys until closed.
		if a.helpOpen {
			switch msg.String() {
			case "?", "esc", "q":
				a.helpOpen = false
			}
			return a, nil
		}
		// When the active view has a focused text input (e.g. the Agents chat or
		// a Secrets value entry), it captures ALL other keys — navigation
		// shortcuts (q, Tab, 1-9, F1-F9, ?) are disabled so the user can type
		// freely. Setup mode likewise owns its own input.
		if !a.setupMode && !a.activeViewInputFocused() {
			switch msg.String() {
			case "q":
				return a, tea.Quit
			case "tab":
				a.cycleSidebar(1)
				return a, a.views[a.activeView].Init()
			case "shift+tab":
				a.cycleSidebar(-1)
				return a, a.views[a.activeView].Init()
			case "?":
				a.helpOpen = true
				return a, nil
			}
			// F1-F9 / 1-9 jump directly.
			if id, ok := directJump(msg.String()); ok {
				a.SwitchView(id)
				return a, a.views[a.activeView].Init()
			}
		}
	}

	// Delegate to the active view.
	var cmd tea.Cmd
	if v, ok := a.views[a.activeView]; ok {
		a.views[a.activeView], cmd = v.Update(msg)
	}
	return a, cmd
}

// View composes the full layout: top bar, sidebar + content, help bar. The
// tutorial and help overlays replace the content area while visible.
func (a *App) View() string {
	if a.setupMode {
		// The wizard takes the whole screen.
		return a.views[ViewSetup].View()
	}
	if a.tutorialStep >= 0 && a.tutorialStep < len(tutorialSteps) {
		return a.overlayBox("Welcome to VORTEX", a.tutorialBody())
	}
	if a.helpOpen {
		return a.overlayBox("Help — "+viewName(a.activeView), a.helpBody())
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

// costPill renders the AI cost indicator: "free" for Ollama, else 💰 $X.XX
// colored green (<$1), amber ($1-5), or red (>$5).
func costPill(c *tui.AICostData) string {
	if c.Free {
		return tui.Pill(brand.IconCost+" free", brand.ColorSuccess)
	}
	color := brand.ColorSuccess
	switch {
	case c.TotalUSD > 5:
		color = brand.ColorDanger
	case c.TotalUSD >= 1:
		color = brand.ColorWarning
	}
	return tui.Pill(fmt.Sprintf("%s $%.2f", brand.IconCost, c.TotalUSD), color)
}

// topBar renders: ▲ VORTEX  v1.0.0  ●cluster  uptime  💰cost  [Tab] views.
func (a *App) topBar() string {
	parts := []string{brand.StyleTitle.Render(brand.LogoSmall)}
	if a.health != nil {
		parts = append(parts,
			brand.StyleSubtitle.Render(a.health.Version),
			tui.Pill(a.health.ClusterName, brand.ColorSuccess),
			brand.StyleSubtitle.Render(a.health.Uptime),
		)
	} else {
		parts = append(parts,
			brand.StyleSubtitle.Render(brand.Version),
			brand.StyleError.Render("disconnected"),
		)
	}
	if a.workingDir != "" {
		parts = append(parts, tui.Pill(brand.IconFolder+" "+a.workingDir, brand.ColorPurple))
	}
	if a.cost != nil {
		parts = append(parts, costPill(a.cost))
	}
	parts = append(parts, brand.StyleHelp.Render("[Tab] views  [q] quit"))
	return strings.Join(parts, "  ")
}

// sidebar renders the left navigation: selected item highlighted with the
// brand selection background, others dim.
func (a *App) sidebar() string {
	var b strings.Builder
	for i, item := range sidebarItems {
		if i == a.selected {
			b.WriteString(brand.StyleSelected.Width(sidebarWidth).Render("▶ "+item.Label) + "\n")
		} else {
			b.WriteString(brand.StyleSubtitle.Render("  "+item.Label) + "\n")
		}
	}
	return lipgloss.NewStyle().Width(sidebarWidth).Render(b.String())
}

// helpBar renders context-sensitive hints for the active view.
func (a *App) helpBar() string {
	hints := map[ViewID]string{
		ViewAgents:  "[Enter] Send  [↑↓] History  [?] Help",
		ViewCode:    "[Enter] Send  [P] Pause  [S] Stop  [?] Help",
		ViewRoutes:  "[Enter] Detail  [r] Reload  [?] Help",
		ViewLogs:    "[f] Filter  [F] Follow  [c] Clear  [?] Help",
		ViewSecrets: "[s] Set  [j/k] Move  [?] Help",
	}
	h := hints[a.activeView]
	if h == "" {
		h = "[Tab] Navigate  [1-9] Jump  [?] Help  [q] Quit"
	}
	return brand.StyleHelp.Render(h)
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

// HelpOpen reports whether the help overlay is visible (for tests).
func (a *App) HelpOpen() bool { return a.helpOpen }

// TutorialActive reports whether the first-run tutorial is visible (for tests).
func (a *App) TutorialActive() bool { return a.tutorialStep >= 0 }

// directJump maps "1".."9" / "f1".."f9" to a ViewID (sidebar order).
func directJump(key string) (ViewID, bool) {
	key = strings.TrimPrefix(key, "f")
	for i, item := range sidebarItems {
		if key == fmt.Sprintf("%d", i+1) {
			return item.ID, true
		}
	}
	return 0, false
}

// viewName returns the sidebar label for a view.
func viewName(id ViewID) string {
	for _, item := range sidebarItems {
		if item.ID == id {
			return item.Label
		}
	}
	return "VORTEX"
}

// --- help overlay -------------------------------------------------------

// helpViewID maps the app's ViewID onto the views package's HelpViewID so the
// help content has one source of truth (views.GetHelp).
func helpViewID(id ViewID) views.HelpViewID {
	switch id {
	case ViewAgents:
		return views.HelpAgents
	case ViewCode:
		return views.HelpCode
	case ViewLogs:
		return views.HelpLogs
	case ViewMetrics:
		return views.HelpMetrics
	case ViewRoutes:
		return views.HelpRoutes
	case ViewNodes:
		return views.HelpNodes
	case ViewSecurity:
		return views.HelpSecurity
	case ViewSecrets:
		return views.HelpSecrets
	default:
		return views.HelpOverview
	}
}

// helpBody renders the overlay content for the active view from the shared
// help catalog (views.GetHelp).
func (a *App) helpBody() string {
	content := views.GetHelp(helpViewID(a.activeView))
	var b strings.Builder
	for _, sec := range content.Sections {
		b.WriteString(brand.StyleTitle.Render(sec.Title) + "\n")
		for _, it := range sec.Items {
			switch {
			case it.Example != "":
				b.WriteString("  " + brand.StyleSubtitle.Render(`"`+it.Example+`"`) + "\n")
			case it.Key != "":
				b.WriteString(fmt.Sprintf("  %-12s %s\n", it.Key, it.Action))
			default:
				b.WriteString("  " + it.Action + "\n")
			}
		}
		b.WriteString("\n")
	}
	b.WriteString(brand.StyleSubtitle.Render("Press ? or Esc to close"))
	return b.String()
}

// --- first-run tutorial ---------------------------------------------------

// tutorialSteps are the 5 first-run hints, shown one per screen.
var tutorialSteps = []string{
	"This is the sidebar. Use Tab or 1-9 to navigate.",
	"This is the main content area.",
	"Try the Agents view — press 2",
	"Type a task and press Enter",
	"You're ready! Press ? for help anytime.",
}

// tutorialDonePath is the first-run flag file.
func tutorialDonePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "vortex", "tutorial-done")
}

// startTutorialIfFirstRun activates the tutorial when its done-flag is absent.
func (a *App) startTutorialIfFirstRun() {
	if _, err := os.Stat(tutorialDonePath()); err == nil {
		a.tutorialStep = -1
		return
	}
	a.tutorialStep = 0
}

// finishTutorial dismisses the tutorial and writes the done flag.
func (a *App) finishTutorial() {
	a.tutorialStep = -1
	path := tutorialDonePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		_ = os.WriteFile(path, []byte("done\n"), 0o600)
	}
}

// tutorialBody renders the current tutorial step.
func (a *App) tutorialBody() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Step %d of %d\n\n", a.tutorialStep+1, len(tutorialSteps)))
	b.WriteString(tutorialSteps[a.tutorialStep] + "\n\n")
	b.WriteString(brand.StyleSubtitle.Render("→ next  ·  Esc skip"))
	return b.String()
}

// overlayBox renders a centered full-screen overlay with a titled border.
func (a *App) overlayBox(title, body string) string {
	box := brand.StyleActive.Padding(1, 2).Render(
		brand.StyleTitle.Render(title) + "\n\n" + body)
	w, h := maxInt(a.width, 40), maxInt(a.height-1, 10)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
