package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
)

func loadedRoutes() RoutesModel {
	m := NewRoutes(nil)
	updated, _ := m.Update(routesData{routes: sampleHealth().Routes})
	return updated.(RoutesModel)
}

func TestRoutes_Init(t *testing.T) {
	if NewRoutes(nil).Init() == nil {
		t.Error("Init should return a fetch command")
	}
}

func TestRoutes_ListsRoutes(t *testing.T) {
	m := loadedRoutes()
	out := m.View()
	if !strings.Contains(out, "api") || !strings.Contains(out, "redis") {
		t.Errorf("view should list routes:\n%s", out)
	}
}

func TestRoutes_Navigation(t *testing.T) {
	m := loadedRoutes()
	if m.Selected() != 0 {
		t.Fatalf("initial selection = %d, want 0", m.Selected())
	}
	down, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if down.(RoutesModel).Selected() != 1 {
		t.Errorf("j should move selection to 1, got %d", down.(RoutesModel).Selected())
	}
	up, _ := down.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if up.(RoutesModel).Selected() != 0 {
		t.Errorf("k should move back to 0, got %d", up.(RoutesModel).Selected())
	}
}

func TestRoutes_DetailToggle(t *testing.T) {
	m := loadedRoutes()
	det, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !det.(RoutesModel).DetailOpen() {
		t.Error("Enter should open detail")
	}
	if !strings.Contains(det.View(), "ROUTE DETAIL") {
		t.Errorf("detail view should show route detail:\n%s", det.View())
	}
	back, _ := det.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if back.(RoutesModel).DetailOpen() {
		t.Error("Esc should close detail")
	}
}

func TestRoutes_EmptyState(t *testing.T) {
	m := NewRoutes(nil)
	updated, _ := m.Update(routesData{routes: nil})
	if !strings.Contains(updated.View(), "no routes") {
		t.Errorf("empty routes should show placeholder:\n%s", updated.View())
	}
}

func TestNodes_Init(t *testing.T) {
	if NewNodes(nil).Init() == nil {
		t.Error("Init should return a fetch command")
	}
}

func TestNodes_LoadingState(t *testing.T) {
	if !strings.Contains(NewNodes(nil).View(), "Loading") {
		t.Error("fresh nodes should show loading")
	}
}

func TestNodes_RendersNode(t *testing.T) {
	m := NewNodes(nil)
	updated, _ := m.Update(nodesData{
		health: sampleHealth(),
		status: &tui.StatusData{NodeID: "8a9fd9be", TLSProvider: "internal", PluginCount: 0, AuditCount: 12, TrustDomain: "c1.vortex"},
	})
	out := updated.View()
	if !strings.Contains(out, "8a9fd9be") || !strings.Contains(out, "single-node") {
		t.Errorf("node view should show node id + mode:\n%s", out)
	}
	if !strings.Contains(out, "Routes:") {
		t.Errorf("node view should list routes:\n%s", out)
	}
}

func TestNodes_ErrorState(t *testing.T) {
	m := NewNodes(nil)
	updated, _ := m.Update(nodesData{err: errString("x")})
	if !strings.Contains(updated.View(), "Could not load nodes") {
		t.Errorf("error should render:\n%s", updated.View())
	}
}
