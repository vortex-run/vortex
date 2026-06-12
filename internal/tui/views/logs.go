package views

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vortex-run/vortex/internal/tui"
)

// LogLine is one displayed log entry.
type LogLine struct {
	Time   string
	Level  string // INFO WARN ERROR DEBUG
	Msg    string
	Fields map[string]string
	Raw    string
}

// LogsModel is the live log viewer with substring filtering and follow mode.
type LogsModel struct {
	client    *tui.Client
	lines     []LogLine
	viewport  viewport.Model
	filter    textinput.Model
	filtering bool // filter input focused
	follow    bool
	width     int
	height    int
}

// NewLogs constructs the logs model.
func NewLogs(client *tui.Client) LogsModel {
	f := textinput.New()
	f.Placeholder = "filter…"
	f.Prompt = "Filter: "
	return LogsModel{
		client:   client,
		viewport: viewport.New(0, 0),
		filter:   f,
		follow:   true,
	}
}

// Init returns no startup command (logs are pushed via SetLines / refresh).
func (m LogsModel) Init() tea.Cmd { return nil }

// SetLines replaces the displayed lines (used by the app's refresh wiring).
func (m *LogsModel) SetLines(lines []LogLine) {
	m.lines = lines
	m.viewport.SetContent(m.renderLines())
	if m.follow {
		m.viewport.GotoBottom()
	}
}

// Update handles keys and resizing.
func (m LogsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.viewport.Width = msg.Width
		m.viewport.Height = max(msg.Height-5, 1)
		m.filter.Width = msg.Width - 10
		m.viewport.SetContent(m.renderLines())

	case tea.KeyMsg:
		if m.filtering {
			switch msg.String() {
			case "enter", "esc":
				m.filtering = false
				m.filter.Blur()
				m.viewport.SetContent(m.renderLines())
				return m, nil
			}
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.viewport.SetContent(m.renderLines())
			return m, cmd
		}
		switch msg.String() {
		case "f":
			m.filtering = true
			m.filter.Focus()
			return m, nil
		case "F":
			m.follow = !m.follow
			if m.follow {
				m.viewport.GotoBottom()
			}
			return m, nil
		case "c":
			m.lines = nil
			m.viewport.SetContent("")
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// View renders the logs screen.
func (m LogsModel) View() string {
	live := tui.StatusDot(true) + " LIVE"
	if !m.follow {
		live = tui.SubtitleStyle.Render("○ PAUSED")
	}
	header := tui.TitleStyle.Render("LOGS") + "  " + live + "  " +
		tui.HelpStyle.Render("[f]Filter [F]Follow [c]Clear")
	parts := []string{header}
	if m.filtering || m.filter.Value() != "" {
		parts = append(parts, m.filter.View())
	}
	body := m.viewport.View()
	if body == "" {
		body = m.renderLines()
	}
	parts = append(parts, body)
	return strings.Join(parts, "\n")
}

// filtered returns the lines matching the current filter substring.
func (m LogsModel) filtered() []LogLine {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if q == "" {
		return m.lines
	}
	var out []LogLine
	for _, l := range m.lines {
		hay := strings.ToLower(l.Msg + " " + l.Raw)
		for k, v := range l.Fields {
			hay += " " + strings.ToLower(k+"="+v)
		}
		if strings.Contains(hay, q) {
			out = append(out, l)
		}
	}
	return out
}

// renderLines renders the (filtered) lines with level coloring.
func (m LogsModel) renderLines() string {
	lines := m.filtered()
	if len(lines) == 0 {
		return tui.SubtitleStyle.Render("(no log lines)")
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(levelStyle(l.Level).Render(l.Time+" "+pad(l.Level, 5)) + " " + l.Msg)
		for k, v := range l.Fields {
			b.WriteString(" " + tui.SubtitleStyle.Render(k+"="+v))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// levelStyle colors a level: WARN=amber, ERROR=red, DEBUG/INFO=dim — INFO is
// the noise floor, so coloring it would drown out the levels that matter.
func levelStyle(level string) lipgloss.Style {
	switch strings.ToUpper(level) {
	case "WARN", "WARNING":
		return tui.StatusWarnStyle
	case "ERROR":
		return tui.StatusErrorStyle
	default:
		return tui.SubtitleStyle
	}
}

func pad(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
}

// Follow reports follow mode (for tests).
func (m LogsModel) Follow() bool { return m.follow }

// Filtering reports whether the filter input is focused (for tests).
func (m LogsModel) Filtering() bool { return m.filtering }

// LineCount returns the number of stored lines (for tests).
func (m LogsModel) LineCount() int { return len(m.lines) }
