// Package views implements the individual screens of the VORTEX terminal UI.
// Each screen is a Bubble Tea model (Init/Update/View). This file is the
// Overview (home) screen.
package views

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vortex-run/vortex/internal/tui"
)

// OverviewModel is the home screen: stat cards, routes, system status, and
// recent audit entries.
type OverviewModel struct {
	client  *tui.Client
	health  *tui.HealthData
	status  *tui.StatusData
	agents  *tui.AgentsData
	audit   *tui.AuditData
	loading bool
	err     error
	width   int
	height  int
}

// NewOverview constructs the overview model.
func NewOverview(client *tui.Client) OverviewModel {
	return OverviewModel{client: client, loading: true}
}

// overviewData carries a refresh result.
type overviewData struct {
	health *tui.HealthData
	status *tui.StatusData
	agents *tui.AgentsData
	audit  *tui.AuditData
	err    error
}

// RefreshMsg is emitted by the app's ticker to request a data refresh.
type RefreshMsg struct{}

// fetchOverview returns a command that loads all overview data.
func (m OverviewModel) fetchOverview() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		if c == nil {
			return overviewData{err: fmt.Errorf("no client")}
		}
		d := overviewData{}
		d.health, d.err = c.Health()
		if d.err != nil {
			return d
		}
		d.status, _ = c.Status()
		d.agents, _ = c.Agents()
		d.audit, _ = c.Audit(5)
		return d
	}
}

// Init fires the initial data fetch.
func (m OverviewModel) Init() tea.Cmd { return m.fetchOverview() }

// Update handles messages.
func (m OverviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case RefreshMsg:
		return m, m.fetchOverview()
	case overviewData:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.health, m.status, m.agents, m.audit = msg.health, msg.status, msg.agents, msg.audit
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "r":
			return m, m.fetchOverview()
		}
	}
	return m, nil
}

// View renders the overview.
func (m OverviewModel) View() string {
	if m.err != nil {
		return tui.StatusErrorStyle.Render("⚠ Could not load overview: " + m.err.Error())
	}
	if m.loading || m.health == nil {
		return tui.SubtitleStyle.Render("Loading overview…")
	}

	var b strings.Builder
	b.WriteString(tui.TitleStyle.Render("Overview") + "\n\n")
	b.WriteString(m.statCards() + "\n\n")
	b.WriteString(m.routesTable() + "\n")
	b.WriteString(m.systemStatus() + "\n")
	b.WriteString(m.recentAudit())
	return b.String()
}

// statCards renders the four top stat cards.
func (m OverviewModel) statCards() string {
	activeConns := int64(0)
	for _, r := range m.health.Routes {
		activeConns += r.Active
	}
	provider := m.health.ClusterName
	if m.status != nil && m.status.TLSProvider != "" {
		provider = m.status.TLSProvider
	}
	cards := []string{
		statCard("Routes", fmt.Sprintf("%d", len(m.health.Routes))),
		statCard("Active Conn", fmt.Sprintf("%d", activeConns)),
		statCard("Uptime", m.health.Uptime),
		statCard("TLS/Provider", provider),
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cards...)
}

// statCard renders a single labelled card.
func statCard(label, value string) string {
	body := tui.SubtitleStyle.Render(label) + "\n" + tui.TitleStyle.Render(value)
	return tui.BorderStyle.Width(16).Padding(0, 1).Render(body)
}

// routesTable renders the routes list.
func (m OverviewModel) routesTable() string {
	var b strings.Builder
	b.WriteString(tui.TableHeaderStyle.Render(fmt.Sprintf("%-12s %-8s %-8s %-8s\n", "NAME", "PROTOCOL", "LISTEN", "ACTIVE")))
	if len(m.health.Routes) == 0 {
		b.WriteString(tui.SubtitleStyle.Render("  (no routes configured)\n"))
		return b.String()
	}
	for _, r := range m.health.Routes {
		b.WriteString(tui.TableRowStyle.Render(fmt.Sprintf("%-12s %-8s %-8s %-8d\n",
			truncateStr(r.Name, 12), r.Protocol, r.Listen, r.Active)))
	}
	return b.String()
}

// systemStatus renders the status dots row.
func (m OverviewModel) systemStatus() string {
	if m.status == nil {
		return ""
	}
	mtls := m.status.TrustDomain != ""
	lines := []string{
		tui.StatusDot(mtls) + " mTLS: " + boolStr(mtls, "enabled", "off") + "   " +
			tui.StatusDot(true) + " Policy: " + boolStr(m.status.PolicyDefault, "default", "custom"),
		tui.StatusDot(true) + fmt.Sprintf(" Plugins: %d installed   ", m.status.PluginCount) +
			tui.StatusDot(true) + fmt.Sprintf(" Audit: %d entries", m.status.AuditCount),
	}
	return strings.Join(lines, "\n") + "\n"
}

// recentAudit renders the last few audit entries.
func (m OverviewModel) recentAudit() string {
	if m.audit == nil || len(m.audit.Entries) == 0 {
		return tui.SubtitleStyle.Render("No recent events.")
	}
	var b strings.Builder
	b.WriteString(tui.TableHeaderStyle.Render(fmt.Sprintf("%-10s %-8s %-16s %s\n", "TIME", "ACTOR", "ACTION", "RESOURCE")))
	for _, e := range m.audit.Entries {
		b.WriteString(tui.TableRowStyle.Render(fmt.Sprintf("%-10s %-8s %-16s %s\n",
			shortTime(e.Timestamp), truncateStr(e.Actor, 8), truncateStr(e.Action, 16), e.Resource)))
	}
	return b.String()
}

// --- helpers ----------------------------------------------------------------

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// shortTime extracts HH:MM:SS from an RFC3339 timestamp.
func shortTime(ts string) string {
	if i := strings.IndexByte(ts, 'T'); i >= 0 && len(ts) >= i+9 {
		return ts[i+1 : i+9]
	}
	if len(ts) > 8 {
		return ts[:8]
	}
	return ts
}
