package views

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vortex-run/vortex/internal/tui"
)

// MetricsModel renders live metrics from the Prometheus endpoint.
type MetricsModel struct {
	client *tui.Client
	data   *tui.MetricsData
	err    error
	width  int
	height int
}

// NewMetrics constructs the metrics model.
func NewMetrics(client *tui.Client) MetricsModel {
	return MetricsModel{client: client}
}

// metricsData carries a refresh result.
type metricsData struct {
	data *tui.MetricsData
	err  error
}

// fetch returns a command loading the metrics.
func (m MetricsModel) fetch() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		if c == nil {
			return metricsData{err: fmt.Errorf("no client")}
		}
		d, err := c.Metrics()
		return metricsData{data: d, err: err}
	}
}

// Init fires the initial fetch.
func (m MetricsModel) Init() tea.Cmd { return m.fetch() }

// Update handles refresh and resize.
func (m MetricsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case RefreshMsg:
		return m, m.fetch()
	case metricsData:
		m.data, m.err = msg.data, msg.err
	}
	return m, nil
}

// View renders the metrics dashboard.
func (m MetricsModel) View() string {
	if m.err != nil {
		return tui.StatusErrorStyle.Render("⚠ Could not load metrics: " + m.err.Error())
	}
	if m.data == nil {
		return tui.SubtitleStyle.Render("Loading metrics…")
	}

	var b strings.Builder
	b.WriteString(tui.TitleStyle.Render("METRICS") + "  " + tui.SubtitleStyle.Render("↻ 10s") + "\n\n")

	b.WriteString(tui.TableHeaderStyle.Render("Requests Total") + "\n")
	b.WriteString(kvBlock(m.data.RequestsTotal))
	b.WriteString("\n")

	b.WriteString(tui.TableHeaderStyle.Render("Active Connections") + "\n")
	b.WriteString(kvBlock(m.data.ActiveConns))
	b.WriteString("\n")

	if len(m.data.P99LatencyMs) > 0 {
		b.WriteString(tui.TableHeaderStyle.Render("P99 Latency (ms)") + "\n")
		b.WriteString(barBlock(m.data.P99LatencyMs))
		b.WriteString("\n")
	}

	b.WriteString(tui.SubtitleStyle.Render(fmt.Sprintf("Cluster Members: %.0f", m.data.ClusterMembers)))
	return b.String()
}

// kvBlock renders a route→value map as aligned rows, sorted by route.
func kvBlock(m map[string]float64) string {
	if len(m) == 0 {
		return tui.SubtitleStyle.Render("  (no data)\n")
	}
	keys := sortedKeys(m)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(tui.TableRowStyle.Render(fmt.Sprintf("  %-12s %10.0f\n", truncateStr(k, 12), m[k])))
	}
	return b.String()
}

// barBlock renders a route→value map as brand progress bars, colored by how
// close each value is to the worst (green <50%, amber 50-80%, red >80%).
func barBlock(m map[string]float64) string {
	if len(m) == 0 {
		return ""
	}
	maxV := 0.0
	for _, v := range m {
		if v > maxV {
			maxV = v
		}
	}
	if maxV == 0 {
		maxV = 1
	}
	var b strings.Builder
	for _, k := range sortedKeys(m) {
		pct := m[k] / maxV * 100
		b.WriteString(fmt.Sprintf("  %-12s %s %s\n",
			truncateStr(k, 12), thresholdBar(pct, 20),
			thresholdStyle(pct).Render(fmt.Sprintf("%6.0fms", m[k]))))
	}
	return b.String()
}

// thresholdBar renders a brand progress bar with the fill colored by load.
func thresholdBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct/100*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	return thresholdStyle(pct).Render(strings.Repeat("█", filled)) +
		tui.SubtitleStyle.Render(strings.Repeat("░", width-filled))
}

// thresholdStyle maps a capacity percentage to green/amber/red.
func thresholdStyle(pct float64) lipgloss.Style {
	switch {
	case pct > 80:
		return tui.StatusErrorStyle
	case pct >= 50:
		return tui.StatusWarnStyle
	default:
		return tui.StatusOKStyle
	}
}

// sortedKeys returns the map keys sorted.
func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
