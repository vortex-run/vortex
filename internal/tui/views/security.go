package views

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vortex-run/vortex/internal/tui"
)

// SecurityModel shows the security posture and a 0-100 score.
type SecurityModel struct {
	client  *tui.Client
	status  *tui.StatusData
	secrets *tui.SecretsData
	err     error
	width   int
	height  int
}

// NewSecurity constructs the security model.
func NewSecurity(client *tui.Client) SecurityModel {
	return SecurityModel{client: client}
}

// securityData carries a refresh result.
type securityData struct {
	status  *tui.StatusData
	secrets *tui.SecretsData
	err     error
}

// fetch loads status + secrets.
func (m SecurityModel) fetch() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		if c == nil {
			return securityData{err: fmt.Errorf("no client")}
		}
		s, err := c.Status()
		if err != nil {
			return securityData{err: err}
		}
		secrets, _ := c.Secrets()
		return securityData{status: s, secrets: secrets}
	}
}

// Init fires the initial fetch.
func (m SecurityModel) Init() tea.Cmd { return m.fetch() }

// Update handles refresh + resize.
func (m SecurityModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case RefreshMsg:
		return m, m.fetch()
	case securityData:
		m.err = msg.err
		if msg.err == nil {
			m.status, m.secrets = msg.status, msg.secrets
		}
	}
	return m, nil
}

// View renders the security screen.
func (m SecurityModel) View() string {
	if m.err != nil {
		return tui.StatusErrorStyle.Render("⚠ Could not load security: " + m.err.Error())
	}
	if m.status == nil {
		return tui.SubtitleStyle.Render("Loading security…")
	}

	score, breakdown := m.score()
	var b strings.Builder
	b.WriteString(tui.TitleStyle.Render("SECURITY") + "  " + scoreStyle(score).Render(fmt.Sprintf("Score: %d/100", score)) + "\n\n")

	mtls := m.status.TrustDomain != ""
	b.WriteString(tui.StatusDot(mtls) + " mTLS: " + boolStr(mtls, "enabled", "off") + "\n")
	if mtls {
		b.WriteString("  Node ID: " + m.status.NodeID + "\n")
		b.WriteString("  Trust domain: " + m.status.TrustDomain + "\n")
	}
	b.WriteString(tui.StatusDot(true) + " TLS provider: " + m.status.TLSProvider + "\n")
	b.WriteString(tui.StatusDot(true) + " Policy: " + boolStr(m.status.PolicyDefault, "default allow-all", "custom") + "\n")
	allSet := m.secretsAllSet()
	b.WriteString(tui.StatusDot(allSet) + " Secrets: " + m.secretSummary() + "\n")
	b.WriteString(tui.StatusDot(true) + fmt.Sprintf(" Audit: %d entries\n", m.status.AuditCount))

	b.WriteString("\n" + tui.SubtitleStyle.Render("Score breakdown:") + "\n")
	for _, line := range breakdown {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// score computes the 0-100 security score and a breakdown.
func (m SecurityModel) score() (int, []string) {
	score := 0
	var lines []string

	add := func(ok bool, points int, label string) {
		dot := tui.StatusDot(ok)
		if ok {
			score += points
		}
		lines = append(lines, fmt.Sprintf("%s +%d %s", dot, points, label))
	}

	add(m.status.TrustDomain != "", 20, "mTLS enabled")
	add(m.secretsAllSet(), 20, "secrets all set")
	add(!m.status.PolicyDefault, 20, "custom policy loaded")
	add(m.status.TLSProvider == "internal" || m.status.TLSProvider != "", 20, "TLS configured")
	add(m.status.AuditCount > 0, 20, "audit log active")
	return score, lines
}

// secretsAllSet reports whether every declared secret is set.
func (m SecurityModel) secretsAllSet() bool {
	if m.secrets == nil || len(m.secrets.Secrets) == 0 {
		return false
	}
	for _, s := range m.secrets.Secrets {
		if !s.Set {
			return false
		}
	}
	return true
}

// secretSummary renders "N/M set".
func (m SecurityModel) secretSummary() string {
	if m.secrets == nil {
		return "unknown"
	}
	set := 0
	for _, s := range m.secrets.Secrets {
		if s.Set {
			set++
		}
	}
	return fmt.Sprintf("%d/%d set", set, len(m.secrets.Secrets))
}

// scoreStyle colors the score: green high, amber mid, red low.
func scoreStyle(score int) lipgloss.Style {
	switch {
	case score >= 80:
		return tui.StatusOKStyle
	case score >= 50:
		return tui.StatusWarnStyle
	default:
		return tui.StatusErrorStyle
	}
}

// Score exposes the computed score (for tests).
func (m SecurityModel) Score() int {
	s, _ := m.score()
	return s
}
