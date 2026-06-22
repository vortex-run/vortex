package views

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vortex-run/vortex/internal/tui"
	"github.com/vortex-run/vortex/internal/tui/brand"
)

// KeysModel is the live API-key-rotation panel: per-slot score, latency,
// spend, and active marker, refreshed every 30s (driven by the app tick).
type KeysModel struct {
	client   *tui.Client
	data     *tui.KeyStatusData
	err      error
	selected int  // selected slot row
	detail   bool // detail overlay open
	width    int
	height   int
}

// NewKeys constructs the keys view.
func NewKeys(client *tui.Client) KeysModel {
	return KeysModel{client: client}
}

// keysData carries a refresh result.
type keysData struct {
	data *tui.KeyStatusData
	err  error
}

// fetch loads the key status.
func (m KeysModel) fetch() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		if c == nil {
			return keysData{err: fmt.Errorf("not connected")}
		}
		d, err := c.KeyStatus()
		return keysData{data: d, err: err}
	}
}

// Init fires the initial fetch.
func (m KeysModel) Init() tea.Cmd { return m.fetch() }

// Update handles refresh, navigation, and the detail overlay.
func (m KeysModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case RefreshMsg:
		return m, m.fetch()
	case keysData:
		m.data, m.err = msg.data, msg.err
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if m.data != nil && m.selected < len(m.data.Slots)-1 {
				m.selected++
			}
		case "k", "up":
			if m.selected > 0 {
				m.selected--
			}
		case "enter":
			if m.data != nil && len(m.data.Slots) > 0 {
				m.detail = !m.detail
			}
		case "esc":
			m.detail = false
		}
	}
	return m, nil
}

// View renders the keys panel (or the detail overlay).
func (m KeysModel) View() string {
	if m.err != nil {
		return brand.StyleError.Render(brand.IconError + " Could not load key status: " + m.err.Error())
	}
	if m.data == nil {
		return brand.StyleSubtitle.Render("Loading key slots…")
	}
	if len(m.data.Slots) == 0 {
		return brand.StyleTitle.Render("API Key Slots") + "\n\n" +
			brand.StyleSubtitle.Render("Single-provider mode (no key slots configured).") + "\n\n" +
			"Add a slot:  vortex keys add --provider deepseek --key <key>"
	}
	if m.detail {
		return m.renderDetail()
	}
	return m.renderTable()
}

// renderTable renders the slot table with score bars.
func (m KeysModel) renderTable() string {
	var b strings.Builder
	b.WriteString(brand.StyleTitle.Render("API Key Slots") + "    " +
		brand.StyleSubtitle.Render("Mode: "+m.data.Mode) + "    " +
		brand.StyleSubtitle.Render("Total: "+brand.FormatCost(m.data.TotalUSD)) + "\n\n")

	b.WriteString(brand.StyleTableHeader.Render(
		fmt.Sprintf("%-5s %-11s %-8s %-9s %-9s %s", "SLOT", "PROVIDER", "SCORE", "LATENCY", "SPENT", "STATUS")) + "\n")
	b.WriteString(brand.StyleSubtitle.Render(strings.Repeat("─", 58)) + "\n")

	for i, s := range m.data.Slots {
		marker := "  "
		row := fmt.Sprintf("%-5s %-11s %s %-9s %-9s %s",
			slotNum(s.ID), titleCase(s.Provider), scoreBarCell(s.Score),
			latencyCell(s.AvgLatencyMs), brand.FormatCost(s.SpentTodayUSD), statusCell(s))
		if i == m.selected {
			marker = brand.StyleTitle.Render(brand.IconArrow + " ")
			row = brand.StyleSelected.Render(row)
		}
		b.WriteString(marker + row + "\n")
	}
	b.WriteString("\n")
	b.WriteString(brand.StyleSubtitle.Render("Score bar: ███ green >70   ██░ amber 40-70   █░░ red <40   ░░░ disabled") + "\n\n")
	b.WriteString(brand.StyleHelp.Render("[j/k] Move  [Enter] Details  [a] Add (CLI)  [r] Remove (CLI)  [t] Test (CLI)  [m] Mode (CLI)"))
	return b.String()
}

// renderDetail renders the selected slot's detail card.
func (m KeysModel) renderDetail() string {
	s := m.data.Slots[m.selected]
	var b strings.Builder
	marker := brand.IconIdle + " Standby"
	if s.Active {
		marker = brand.IconSuccess + " Active"
	}
	b.WriteString(brand.StyleTitle.Render(fmt.Sprintf("Slot %s — %s (%s)", slotNum(s.ID), titleCase(s.Provider), s.Label)) +
		"  " + marker + "\n\n")
	row := func(k, v string) { b.WriteString(fmt.Sprintf("  %-14s %s\n", k, v)) }
	row("Provider:", s.Provider)
	row("Model:", orDash(s.Model))
	row("API Key:", s.MaskedKey+"  (encrypted at rest)")
	row("Priority:", fmt.Sprintf("%d", s.Priority))
	budget := "unlimited"
	if s.DailyBudget > 0 {
		budget = brand.FormatCost(s.DailyBudget)
	}
	row("Daily limit:", budget)
	b.WriteString("\n")
	row("Health Score:", fmt.Sprintf("%d/100", s.Score))
	row("Requests today:", fmt.Sprintf("%d", s.RequestsToday))
	row("Errors (last 10):", fmt.Sprintf("%d", s.ErrorsLast10))
	row("Avg latency:", latencyCell(s.AvgLatencyMs))
	row("Spent today:", brand.FormatCost(s.SpentTodayUSD))
	if s.RateLimited {
		row("Rate limited:", brand.StyleWarn.Render("yes"))
	}
	b.WriteString("\n" + brand.StyleSubtitle.Render("[Enter/Esc] Back"))
	box := brand.StyleActive.Padding(1, 2).Render(b.String())
	if m.width < 20 {
		return box
	}
	return lipgloss.Place(m.width, maxIntK(m.height-2, 12), lipgloss.Left, lipgloss.Top, box)
}

// --- rendering helpers ------------------------------------------------------

// scoreBarCell renders a colored 3-cell score bar plus the numeric score.
func scoreBarCell(score int) string {
	bar := scoreBar3(score)
	return fmt.Sprintf("%s %3d", bar, score)
}

// scoreBar3 renders a 3-cell bar colored by score band.
func scoreBar3(score int) string {
	color := brand.ColorDanger
	switch {
	case score > 70:
		color = brand.ColorSuccess
	case score >= 40:
		color = brand.ColorWarning
	}
	filled := score * 3 / 100
	if filled > 3 {
		filled = 3
	}
	st := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	return st.Render(strings.Repeat("█", filled)) +
		brand.StyleSubtitle.Render(strings.Repeat("░", 3-filled))
}

// statusCell renders a slot's status label.
func statusCell(s tui.KeySlotData) string {
	switch {
	case !s.Enabled:
		return brand.StyleError.Render(brand.IconError + " Disabled")
	case s.RateLimited:
		return brand.StyleWarn.Render(brand.IconWarn + " Limited")
	case s.Active:
		return brand.StyleSuccess.Render(brand.IconSuccess + " Active")
	default:
		return brand.StyleSubtitle.Render(brand.IconIdle + " Standby")
	}
}

// latencyCell formats a latency, using "local" for free/local providers.
func latencyCell(ms int64) string {
	if ms == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// slotNum renders "slot-1" as "1".
func slotNum(id string) string {
	id = strings.TrimPrefix(id, "slot-")
	return id
}

// titleCase display-cases a provider id.
func titleCase(p string) string {
	switch p {
	case "deepseek":
		return "DeepSeek"
	case "openai":
		return "OpenAI"
	case "openrouter":
		return "OpenRouter"
	case "":
		return "—"
	default:
		return strings.ToUpper(p[:1]) + p[1:]
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func maxIntK(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// SelectedSlot returns the highlighted slot id (for tests).
func (m KeysModel) SelectedSlot() string {
	if m.data == nil || m.selected >= len(m.data.Slots) {
		return ""
	}
	return m.data.Slots[m.selected].ID
}

// DetailOpen reports whether the detail overlay is open (for tests).
func (m KeysModel) DetailOpen() bool { return m.detail }

// SetData sets the status data directly (for tests).
func (m *KeysModel) SetData(d *tui.KeyStatusData) { m.data = d }
