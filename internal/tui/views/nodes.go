package views

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
)

// NodesModel shows cluster node status (single-node aware).
type NodesModel struct {
	client *tui.Client
	health *tui.HealthData
	status *tui.StatusData
	err    error
	width  int
	height int
}

// NewNodes constructs the nodes model.
func NewNodes(client *tui.Client) NodesModel {
	return NodesModel{client: client}
}

// nodesData carries a refresh result.
type nodesData struct {
	health *tui.HealthData
	status *tui.StatusData
	err    error
}

// fetch loads health + status.
func (m NodesModel) fetch() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		if c == nil {
			return nodesData{err: fmt.Errorf("no client")}
		}
		h, err := c.Health()
		if err != nil {
			return nodesData{err: err}
		}
		s, _ := c.Status()
		return nodesData{health: h, status: s}
	}
}

// Init fires the initial fetch.
func (m NodesModel) Init() tea.Cmd { return m.fetch() }

// Update handles refresh + resize.
func (m NodesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case RefreshMsg:
		return m, m.fetch()
	case nodesData:
		m.err = msg.err
		if msg.err == nil {
			m.health, m.status = msg.health, msg.status
		}
	}
	return m, nil
}

// View renders the node panel.
func (m NodesModel) View() string {
	if m.err != nil {
		return tui.StatusErrorStyle.Render("⚠ Could not load nodes: " + m.err.Error())
	}
	if m.health == nil {
		return tui.SubtitleStyle.Render("Loading nodes…")
	}

	var b strings.Builder
	b.WriteString(tui.TitleStyle.Render("NODES") + "  " +
		tui.SubtitleStyle.Render("1 member  Mode: single-node") + "\n\n")

	nodeID := "—"
	if m.status != nil && m.status.NodeID != "" {
		nodeID = m.status.NodeID
	}
	b.WriteString(tui.StatusDot(true) + " node-1  " + nodeID + "  " + m.health.ClusterName + "\n")
	b.WriteString(fmt.Sprintf("  Version: %-12s Uptime: %s\n", m.health.Version, m.health.Uptime))
	if m.status != nil {
		b.WriteString(fmt.Sprintf("  TLS: %-12s Plugins: %d\n", m.status.TLSProvider, m.status.PluginCount))
		b.WriteString(fmt.Sprintf("  Audit: %d entries  Trust: %s\n", m.status.AuditCount, m.status.TrustDomain))
	}

	b.WriteString("\n  Routes:\n")
	for _, r := range m.health.Routes {
		b.WriteString(fmt.Sprintf("  %-12s %-6s %-7s %s\n",
			truncateStr(r.Name, 12), r.Protocol, r.Listen, tui.StatusDot(true)+" active"))
	}
	b.WriteString("\n  " + tui.HelpStyle.Render("[a] Add node  [d] Drain  [Esc] Back"))
	return b.String()
}
