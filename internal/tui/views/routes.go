package views

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vortex-run/vortex/internal/tui"
)

// RoutesModel is the route list + detail panel.
type RoutesModel struct {
	client   *tui.Client
	routes   []tui.RouteData
	selected int
	detail   bool
	err      error
	width    int
	height   int
}

// NewRoutes constructs the routes model.
func NewRoutes(client *tui.Client) RoutesModel {
	return RoutesModel{client: client}
}

// routesData carries a refresh result.
type routesData struct {
	routes []tui.RouteData
	err    error
}

// fetch loads routes from /health.
func (m RoutesModel) fetch() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		if c == nil {
			return routesData{err: fmt.Errorf("no client")}
		}
		h, err := c.Health()
		if err != nil {
			return routesData{err: err}
		}
		return routesData{routes: h.Routes}
	}
}

// Init fires the initial fetch.
func (m RoutesModel) Init() tea.Cmd { return m.fetch() }

// Update handles navigation, detail toggling, and reload.
func (m RoutesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case RefreshMsg:
		return m, m.fetch()
	case routesData:
		m.err = msg.err
		if msg.err == nil {
			m.routes = msg.routes
			if m.selected >= len(m.routes) {
				m.selected = max(len(m.routes)-1, 0)
			}
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if m.selected < len(m.routes)-1 {
				m.selected++
			}
		case "k", "up":
			if m.selected > 0 {
				m.selected--
			}
		case "enter":
			if len(m.routes) > 0 {
				m.detail = true
			}
		case "esc":
			m.detail = false
		case "r":
			return m, tea.Batch(m.fetch(), reloadCmd(m.client))
		}
	}
	return m, nil
}

// View renders either the list or the split list+detail.
func (m RoutesModel) View() string {
	if m.err != nil {
		return tui.StatusErrorStyle.Render("⚠ Could not load routes: " + m.err.Error())
	}
	header := tui.TitleStyle.Render("ROUTES") + "  " +
		tui.SubtitleStyle.Render(fmt.Sprintf("%d active", len(m.routes))) + "  " +
		tui.HelpStyle.Render("[n]New [e]Edit [Enter]Detail")

	list := m.routeList()
	if !m.detail {
		return header + "\n\n" + list
	}
	detail := m.routeDetail()
	return header + "\n\n" + lipgloss.JoinHorizontal(lipgloss.Top,
		tui.BorderStyle.Width(24).Render(list),
		"  ",
		tui.ActiveStyle.Width(40).Render(detail))
}

// routeList renders the selectable route list.
func (m RoutesModel) routeList() string {
	if len(m.routes) == 0 {
		return tui.SubtitleStyle.Render("(no routes configured)")
	}
	var b strings.Builder
	for i, r := range m.routes {
		marker := "  "
		line := fmt.Sprintf("%-12s %-6s %-7s %d active", truncateStr(r.Name, 12),
			strings.ToUpper(r.Protocol), r.Listen, r.Active)
		if i == m.selected {
			marker = "▶ "
			b.WriteString(tui.SelectedStyle.Render(marker+line) + "\n")
		} else {
			b.WriteString(marker + tui.TableRowStyle.Render(line) + "\n")
		}
	}
	return b.String()
}

// routeDetail renders the selected route's detail panel.
func (m RoutesModel) routeDetail() string {
	if m.selected >= len(m.routes) {
		return ""
	}
	r := m.routes[m.selected]
	rows := []string{
		tui.TitleStyle.Render("ROUTE DETAIL: " + r.Name),
		"",
		"Protocol: " + r.Protocol,
		"Listen:   " + r.Listen,
		fmt.Sprintf("Active:   %d connections", r.Active),
		"",
		tui.HelpStyle.Render("[e] Edit  [r] Reload  [Esc] Back"),
	}
	return strings.Join(rows, "\n")
}

// reloadCmd triggers a config reload via the client.
func reloadCmd(c *tui.Client) tea.Cmd {
	return func() tea.Msg {
		if c != nil {
			_ = c.Reload()
		}
		return RefreshMsg{}
	}
}

// Selected returns the selected index (for tests).
func (m RoutesModel) Selected() int { return m.selected }

// DetailOpen reports whether the detail panel is shown (for tests).
func (m RoutesModel) DetailOpen() bool { return m.detail }
