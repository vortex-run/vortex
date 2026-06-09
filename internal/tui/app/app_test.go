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
