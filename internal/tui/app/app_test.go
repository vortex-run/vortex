package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
)

func TestNewApp_Disconnected(t *testing.T) {
	// A client pointing at nothing → starts in setup mode.
	c := tui.NewClient(tui.ClientConfig{BaseURL: "http://127.0.0.1:1"})
	a := NewApp(c)
	if !a.SetupMode() {
		t.Error("disconnected client should start in setup mode")
	}
	if a.ActiveView() != ViewSetup {
		t.Errorf("active view = %d, want ViewSetup", a.ActiveView())
	}
}

func TestNewApp_NilClientSetup(t *testing.T) {
	a := NewApp(nil)
	if a.ActiveView() != ViewSetup {
		t.Errorf("nil client should start in setup, got view %d", a.ActiveView())
	}
}

func TestApp_SwitchView(t *testing.T) {
	a := NewApp(nil)
	a.SwitchView(ViewAgents)
	if a.ActiveView() != ViewAgents {
		t.Errorf("active view = %d, want ViewAgents", a.ActiveView())
	}
	// Sidebar selection should track the active view.
	if sidebarItems[a.selected].ID != ViewAgents {
		t.Errorf("sidebar selection out of sync: %v", sidebarItems[a.selected])
	}
}

func TestApp_QuitKey(t *testing.T) {
	a := connectedApp(t)
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q should return a quit command")
	}
	if msg := cmd(); msg == nil {
		t.Error("quit command should produce a message")
	}
	// tea.Quit produces a QuitMsg.
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q should map to tea.Quit")
	}
}

func TestApp_TabCyclesSidebar(t *testing.T) {
	a := connectedApp(t)
	start := a.ActiveView()
	a.Update(tea.KeyMsg{Type: tea.KeyTab})
	if a.ActiveView() == start {
		t.Error("Tab should advance the active view")
	}
}

func TestApp_DirectJump(t *testing.T) {
	a := connectedApp(t)
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if a.ActiveView() != ViewAgents {
		t.Errorf("'2' should jump to Agents, got %d", a.ActiveView())
	}
}

func TestApp_WindowResizePropagates(t *testing.T) {
	a := connectedApp(t)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if a.width != 120 || a.height != 40 {
		t.Errorf("app size = %dx%d, want 120x40", a.width, a.height)
	}
}

func TestApp_ViewRenders(t *testing.T) {
	a := connectedApp(t)
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	out := a.View()
	if out == "" {
		t.Error("View should render non-empty")
	}
}

// connectedApp builds an app whose client reports connected by pointing it at a
// live fake server is overkill here; instead we force a non-setup app.
func connectedApp(t *testing.T) *App {
	t.Helper()
	a := NewApp(nil)
	a.setupMode = false
	a.activeView = ViewOverview
	a.selected = 0
	return a
}

func TestApp_TopBarShowsWorkingDir(t *testing.T) {
	a := connectedApp(t)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a.workingDir = "/tmp/details"
	a.health = &tui.HealthData{Version: "v1", ClusterName: "c1", Uptime: "1h"}
	if !strings.Contains(a.View(), "/tmp/details") {
		t.Errorf("top bar should show the working dir:\n%s", a.View())
	}
}

func TestCostPill_Colors(t *testing.T) {
	if got := costPill(&tui.AICostData{Free: true}); !strings.Contains(got, "free") {
		t.Errorf("free pill = %q", got)
	}
	if got := costPill(&tui.AICostData{TotalUSD: 0.50}); !strings.Contains(got, "$0.50") {
		t.Errorf("cost pill = %q, want $0.50", got)
	}
	if got := costPill(&tui.AICostData{TotalUSD: 7.0}); !strings.Contains(got, "$7.00") {
		t.Errorf("high cost pill = %q", got)
	}
}

func TestApp_TopBarShowsCost(t *testing.T) {
	a := connectedApp(t)
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	a.health = &tui.HealthData{Version: "v1", ClusterName: "c1", Uptime: "1h"}
	a.cost = &tui.AICostData{TotalUSD: 0.02}
	if !strings.Contains(a.View(), "$0.02") {
		t.Errorf("top bar should show cost:\n%s", a.View())
	}
}

func TestApp_InputFocusedBlocksNavigation(t *testing.T) {
	a := connectedApp(t)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a.SwitchView(ViewAgents) // Agents view reports IsInputFocused()=true
	// Pressing "1" must NOT jump to Overview — it goes to the chat input.
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if a.ActiveView() != ViewAgents {
		t.Errorf("'1' while typing in Agents jumped to %d; should stay on Agents", a.ActiveView())
	}
	// Tab must not cycle the sidebar either.
	a.Update(tea.KeyMsg{Type: tea.KeyTab})
	if a.ActiveView() != ViewAgents {
		t.Error("Tab while typing in Agents should not cycle the sidebar")
	}
	// 'q' must not quit while typing.
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Error("'q' while typing in Agents should not quit")
		}
	}
}

func TestApp_CtrlCQuitsEvenWhileTyping(t *testing.T) {
	a := connectedApp(t)
	a.SwitchView(ViewAgents)
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C should always quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("Ctrl+C should map to tea.Quit even while typing")
	}
}

func TestApp_NavigationWorksWhenNotInputFocused(t *testing.T) {
	a := connectedApp(t) // starts on Overview (no input focus)
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if a.ActiveView() != ViewCode {
		t.Errorf("'3' on a non-input view should jump to Code, got %d", a.ActiveView())
	}
	// The Code view's input is focused on entry and rightly captures digits;
	// Esc blurs it, after which navigation keys work again.
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	if a.ActiveView() != ViewRoutes {
		t.Errorf("'4' after Esc should jump to Routes, got %d", a.ActiveView())
	}
}

func TestApp_TopBarShowsVortexBrand(t *testing.T) {
	a := connectedApp(t)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(a.View(), "▲ VORTEX") {
		t.Errorf("top bar should show the brand mark:\n%s", a.View())
	}
}

func TestApp_SidebarShowsCodeItem(t *testing.T) {
	a := connectedApp(t)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(a.View(), "Code") {
		t.Errorf("sidebar should list the Code view:\n%s", a.View())
	}
}

func TestApp_HelpBarChangesByView(t *testing.T) {
	a := connectedApp(t)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	a.SwitchView(ViewLogs)
	logsBar := a.helpBar()
	if !strings.Contains(logsBar, "[F] Follow") {
		t.Errorf("logs help bar = %q", logsBar)
	}
	a.SwitchView(ViewAgents)
	agentsBar := a.helpBar()
	if !strings.Contains(agentsBar, "[Enter] Send") || agentsBar == logsBar {
		t.Errorf("agents help bar = %q, must differ from logs", agentsBar)
	}
	a.SwitchView(ViewMetrics) // no specific hints → default
	if def := a.helpBar(); !strings.Contains(def, "[?] Help") {
		t.Errorf("default help bar = %q", def)
	}
}

func TestApp_HelpOverlayTogglesWithQuestionMark(t *testing.T) {
	a := connectedApp(t)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if !a.HelpOpen() {
		t.Fatal("? should open the help overlay")
	}
	if out := a.View(); !strings.Contains(out, "Help — Overview") {
		t.Errorf("overlay should title the active view:\n%s", out)
	}
	// While open, navigation keys are swallowed.
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	if a.ActiveView() != ViewOverview {
		t.Error("navigation must be disabled while help is open")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if a.HelpOpen() {
		t.Error("? should close the help overlay")
	}

	// Esc also closes.
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.HelpOpen() {
		t.Error("Esc should close the help overlay")
	}
}

func TestApp_HelpOverlayListsAgentCommands(t *testing.T) {
	a := connectedApp(t)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a.SwitchView(ViewAgents)
	a.helpOpen = true
	out := a.View()
	for _, want := range []string{"/ls", "/read", "/run", "/undo", "Example tasks"} {
		if !strings.Contains(out, want) {
			t.Errorf("agents help overlay missing %q", want)
		}
	}
}

func TestApp_TutorialFirstRunAndFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())

	a := connectedApp(t)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// First run: no flag file → tutorial starts.
	a.startTutorialIfFirstRun()
	if !a.TutorialActive() {
		t.Fatal("tutorial should be active on first run")
	}
	if out := a.View(); !strings.Contains(out, "sidebar") {
		t.Errorf("tutorial step 1 should mention the sidebar:\n%s", out)
	}

	// Advance through all 5 steps with →.
	for i := 0; i < len(tutorialSteps); i++ {
		a.Update(tea.KeyMsg{Type: tea.KeyRight})
	}
	if a.TutorialActive() {
		t.Error("tutorial should finish after the last step")
	}

	// The done flag persists: a fresh check must NOT restart it.
	a.startTutorialIfFirstRun()
	if a.TutorialActive() {
		t.Error("tutorial must not run again once the done flag exists")
	}
}

func TestApp_TutorialEscSkipsAndWritesFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())

	a := connectedApp(t)
	a.startTutorialIfFirstRun()
	if !a.TutorialActive() {
		t.Fatal("tutorial should start")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.TutorialActive() {
		t.Error("Esc should skip the tutorial")
	}
	a.startTutorialIfFirstRun()
	if a.TutorialActive() {
		t.Error("skipping must also write the done flag")
	}
}
