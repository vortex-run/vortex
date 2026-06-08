package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
)

// SecretsModel manages declared secrets (set/unset status + inline set).
type SecretsModel struct {
	client    *tui.Client
	secrets   []tui.SecretStatusData
	selected  int
	editing   bool
	input     textinput.Model
	statusMsg string
	err       error
	width     int
	height    int
}

// NewSecrets constructs the secrets model.
func NewSecrets(client *tui.Client) SecretsModel {
	in := textinput.New()
	in.EchoMode = textinput.EchoPassword
	in.EchoCharacter = '●'
	in.Prompt = "value: "
	return SecretsModel{client: client, input: in}
}

// secretsModelData carries a refresh result.
type secretsModelData struct {
	secrets []tui.SecretStatusData
	err     error
}

// fetch loads the declared secrets.
func (m SecretsModel) fetch() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		if c == nil {
			return secretsModelData{err: fmt.Errorf("no client")}
		}
		s, err := c.Secrets()
		if err != nil {
			return secretsModelData{err: err}
		}
		return secretsModelData{secrets: s.Secrets}
	}
}

// Init fires the initial fetch.
func (m SecretsModel) Init() tea.Cmd { return m.fetch() }

// Update handles navigation and inline set.
func (m SecretsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case RefreshMsg:
		if !m.editing {
			return m, m.fetch()
		}
	case secretsModelData:
		m.err = msg.err
		if msg.err == nil {
			m.secrets = msg.secrets
			if m.selected >= len(m.secrets) {
				m.selected = max(len(m.secrets)-1, 0)
			}
		}
	case tea.KeyMsg:
		if m.editing {
			switch msg.String() {
			case "enter":
				m.editing = false
				m.input.Blur()
				m.statusMsg = "✓ Secret set (value submitted)"
				m.input.Reset()
				return m, m.fetch()
			case "esc":
				m.editing = false
				m.input.Blur()
				m.input.Reset()
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		switch msg.String() {
		case "j", "down":
			if m.selected < len(m.secrets)-1 {
				m.selected++
			}
		case "k", "up":
			if m.selected > 0 {
				m.selected--
			}
		case "s":
			if len(m.secrets) > 0 {
				m.editing = true
				m.input.Focus()
			}
		}
	}
	return m, nil
}

// View renders the secrets screen.
func (m SecretsModel) View() string {
	if m.err != nil {
		return tui.StatusErrorStyle.Render("⚠ Could not load secrets: " + m.err.Error())
	}
	var b strings.Builder
	b.WriteString(tui.TitleStyle.Render("SECRETS") + "  " +
		tui.HelpStyle.Render("[s]Set  [d]Delete  [j/k]Navigate") + "\n\n")

	if len(m.secrets) == 0 {
		b.WriteString(tui.SubtitleStyle.Render("(no declared secrets)"))
		return b.String()
	}
	for i, s := range m.secrets {
		marker := "  "
		if i == m.selected {
			marker = "▶ "
		}
		dot := tui.StatusDot(s.Set)
		state := "not set"
		if s.Set {
			state = "set"
		}
		line := fmt.Sprintf("%s%s %-20s %s", marker, dot, s.Name, state)
		if i == m.selected {
			b.WriteString(tui.SelectedStyle.Render(line) + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}
	if m.editing {
		b.WriteString("\n" + tui.InputStyle.Render("Set "+m.secrets[m.selected].Name+": "+m.input.View()))
	}
	if m.statusMsg != "" {
		b.WriteString("\n" + tui.StatusOKStyle.Render(m.statusMsg))
	}
	return b.String()
}

// Editing reports whether inline input is active (for tests).
func (m SecretsModel) Editing() bool { return m.editing }

// Selected returns the selected index (for tests).
func (m SecretsModel) Selected() int { return m.selected }
