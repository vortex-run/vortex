package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
)

func sampleHealth() *tui.HealthData {
	return &tui.HealthData{
		Status: "ok", Version: "v9", ClusterName: "c1", Uptime: "2h",
		Routes: []tui.RouteData{
			{Name: "api", Protocol: "https", Listen: ":0", Active: 0},
			{Name: "redis", Protocol: "tcp", Listen: ":6379", Active: 2},
		},
	}
}

func TestOverview_Init(t *testing.T) {
	m := NewOverview(nil)
	if cmd := m.Init(); cmd == nil {
		t.Error("Init should return a fetch command")
	}
}

func TestOverview_LoadingState(t *testing.T) {
	m := NewOverview(nil)
	if !strings.Contains(m.View(), "Loading") {
		t.Errorf("fresh model should show loading, got: %s", m.View())
	}
}

func TestOverview_ErrorState(t *testing.T) {
	m := NewOverview(nil)
	updated, _ := m.Update(overviewData{err: errString("boom")})
	if !strings.Contains(updated.View(), "Could not load") {
		t.Errorf("error data should render an error, got: %s", updated.View())
	}
}

func TestOverview_RendersRoutes(t *testing.T) {
	m := NewOverview(nil)
	updated, _ := m.Update(overviewData{
		health: sampleHealth(),
		status: &tui.StatusData{TrustDomain: "c1.vortex", PolicyDefault: true, PluginCount: 0, AuditCount: 7},
	})
	out := updated.View()
	if !strings.Contains(out, "api") || !strings.Contains(out, "redis") {
		t.Errorf("view should list route names, got:\n%s", out)
	}
	if !strings.Contains(out, "Routes") {
		t.Errorf("view should show stat cards, got:\n%s", out)
	}
}

func TestOverview_WindowResize(t *testing.T) {
	m := NewOverview(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	om := updated.(OverviewModel)
	if om.width != 120 || om.height != 40 {
		t.Errorf("resize not applied: %dx%d", om.width, om.height)
	}
}

func TestOverview_ReloadKey(t *testing.T) {
	m := NewOverview(nil)
	// Move out of loading first so the key path is reached.
	updated, _ := m.Update(overviewData{health: sampleHealth()})
	_, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd == nil {
		t.Error("'r' should trigger a refresh command")
	}
}

// errString is a tiny error type for tests.
type errString string

func (e errString) Error() string { return string(e) }
