package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
)

func TestMetrics_Init(t *testing.T) {
	m := NewMetrics(nil)
	if m.Init() == nil {
		t.Error("Init should return a fetch command")
	}
}

func TestMetrics_LoadingState(t *testing.T) {
	m := NewMetrics(nil)
	if !strings.Contains(m.View(), "Loading") {
		t.Errorf("fresh metrics should show loading, got: %s", m.View())
	}
}

func TestMetrics_ErrorState(t *testing.T) {
	m := NewMetrics(nil)
	updated, _ := m.Update(metricsData{err: errString("down")})
	if !strings.Contains(updated.View(), "Could not load metrics") {
		t.Errorf("error should render, got: %s", updated.View())
	}
}

func TestMetrics_RendersData(t *testing.T) {
	m := NewMetrics(nil)
	updated, _ := m.Update(metricsData{data: &tui.MetricsData{
		RequestsTotal:  map[string]float64{"api": 1234, "redis": 234},
		ActiveConns:    map[string]float64{"api": 0},
		ClusterMembers: 1,
	}})
	out := updated.View()
	if !strings.Contains(out, "Requests Total") || !strings.Contains(out, "api") {
		t.Errorf("view should show requests total per route:\n%s", out)
	}
	if !strings.Contains(out, "1234") {
		t.Errorf("view should show the request count:\n%s", out)
	}
	if !strings.Contains(out, "Cluster Members: 1") {
		t.Errorf("view should show cluster members:\n%s", out)
	}
}

func TestMetrics_BarChart(t *testing.T) {
	// A full bar (100%) is all filled blocks; empty is all light blocks.
	full := thresholdBar(100, 10)
	empty := thresholdBar(0, 10)
	if !strings.Contains(full, "█") || strings.Contains(full, "░") {
		t.Errorf("full bar = %q, want all filled", full)
	}
	if strings.Contains(empty, "█") {
		t.Errorf("empty bar = %q, want no filled", empty)
	}
}

func TestMetrics_ThresholdColors(t *testing.T) {
	// Threshold styles must differ across the three bands.
	low, mid, high := thresholdStyle(30), thresholdStyle(65), thresholdStyle(95)
	if low.GetForeground() == high.GetForeground() {
		t.Error("low and high load must use different colors")
	}
	if mid.GetForeground() == low.GetForeground() || mid.GetForeground() == high.GetForeground() {
		t.Error("mid band must have its own color")
	}
}

func TestMetrics_Resize(t *testing.T) {
	m := NewMetrics(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if updated.(MetricsModel).width != 100 {
		t.Error("resize not applied")
	}
}
