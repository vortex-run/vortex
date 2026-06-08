package views

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
)

// SetupStep is a stage in the wizard.
type SetupStep int

// Wizard steps.
const (
	StepWelcome SetupStep = iota
	StepProvider
	StepAPIKey
	StepTelegram
	StepCreatingKey
	StepComplete
)

// providerOption is one selectable AI provider.
type providerOption struct {
	id   string
	name string
	desc string
	cost string
}

// setupProviders are the selectable providers (matching cmd/setup.go).
var setupProviders = []providerOption{
	{"claude", "Anthropic Claude", "Best reasoning, most capable", "$$"},
	{"deepseek", "DeepSeek", "Fast, cost-effective, OpenAI-compatible", "$"},
	{"openai", "OpenAI GPT", "GPT-4o and GPT-4o-mini", "$$"},
	{"gemini", "Google Gemini", "Gemini 1.5 Pro and Flash", "$"},
	{"ollama", "Ollama (Local)", "Run AI models on your own machine", "free"},
}

// SetupModel is the interactive first-run wizard.
type SetupModel struct {
	step     SetupStep
	selected int // provider index
	provider string
	apiKey   textinput.Model
	verified bool
	verifMsg string
	wantsTG  bool
	width    int
	height   int
}

// NewSetup constructs the wizard at the welcome step.
func NewSetup() SetupModel {
	in := textinput.New()
	in.EchoMode = textinput.EchoPassword
	in.EchoCharacter = '●'
	in.Prompt = "key: "
	return SetupModel{step: StepWelcome, apiKey: in}
}

// SetupDoneMsg is emitted when the wizard completes (or is skipped).
type SetupDoneMsg struct {
	Provider string
	APIKey   string
	Skipped  bool
}

// Init returns no startup command.
func (m SetupModel) Init() tea.Cmd { return nil }

// Update advances the wizard.
func (m SetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.apiKey.Width = msg.Width - 10

	case tea.KeyMsg:
		switch m.step {
		case StepWelcome:
			// Any key advances.
			m.step = StepProvider
			return m, nil

		case StepProvider:
			switch msg.String() {
			case "j", "down":
				if m.selected < len(setupProviders)-1 {
					m.selected++
				}
			case "k", "up":
				if m.selected > 0 {
					m.selected--
				}
			case "enter":
				m.provider = setupProviders[m.selected].id
				if m.provider == "ollama" {
					// Ollama needs no key; jump to complete.
					m.step = StepComplete
					return m, m.done(false)
				}
				m.step = StepAPIKey
				m.apiKey.Focus()
			case "s":
				// Skip.
				m.step = StepComplete
				return m, m.done(true)
			}

		case StepAPIKey:
			switch msg.String() {
			case "enter":
				if strings.TrimSpace(m.apiKey.Value()) == "" {
					return m, nil
				}
				m.verified = true
				m.verifMsg = "✓ Key accepted"
				m.step = StepTelegram
				return m, nil
			case "esc":
				m.step = StepProvider
				m.apiKey.Blur()
				return m, nil
			}
			var cmd tea.Cmd
			m.apiKey, cmd = m.apiKey.Update(msg)
			return m, cmd

		case StepTelegram:
			switch strings.ToLower(msg.String()) {
			case "y":
				m.wantsTG = true
				m.step = StepCreatingKey
				return m, m.done(false)
			case "n", "enter", "esc":
				m.step = StepCreatingKey
				return m, m.done(false)
			}

		case StepCreatingKey, StepComplete:
			// Terminal states; any key is a no-op (the app handles exit).
		}
	}
	return m, nil
}

// done emits the completion message and advances to the final step.
func (m SetupModel) done(skipped bool) tea.Cmd {
	provider := m.provider
	key := m.apiKey.Value()
	return func() tea.Msg {
		return SetupDoneMsg{Provider: provider, APIKey: key, Skipped: skipped}
	}
}

// View renders the current step.
func (m SetupModel) View() string {
	switch m.step {
	case StepWelcome:
		return welcomeArt + "\n\n" + tui.SubtitleStyle.Render("Press any key to continue…")
	case StepProvider:
		return m.providerView()
	case StepAPIKey:
		return m.apiKeyView()
	case StepTelegram:
		return tui.TitleStyle.Render("Telegram alerts") + "\n\n" +
			"Would you like to set up Telegram alerts? " + tui.SubtitleStyle.Render("[y/N]")
	case StepCreatingKey:
		return tui.SubtitleStyle.Render("⠸ Creating VORTEX API key…")
	default:
		return completeArt
	}
}

// providerView renders the selectable provider list.
func (m SetupModel) providerView() string {
	var b strings.Builder
	b.WriteString(tui.TitleStyle.Render("Select your AI provider") + "\n\n")
	for i, p := range setupProviders {
		marker := "  "
		line := p.name + "  " + tui.SubtitleStyle.Render(p.desc+" ("+p.cost+")")
		if i == m.selected {
			marker = "▶ "
			b.WriteString(tui.SelectedStyle.Render(marker+line) + "\n")
		} else {
			b.WriteString(marker + line + "\n")
		}
	}
	b.WriteString("\n" + tui.HelpStyle.Render("[↑/↓] Navigate  [Enter] Select  [s] Skip"))
	return b.String()
}

// apiKeyView renders the masked key-input step.
func (m SetupModel) apiKeyView() string {
	p := setupProviders[m.selected]
	url := providerURL(p.id)
	var b strings.Builder
	b.WriteString(tui.TitleStyle.Render("Enter your "+p.name+" API key") + "\n\n")
	b.WriteString(tui.SubtitleStyle.Render("Get your key at: "+url) + "\n\n")
	b.WriteString(tui.InputStyle.Render(m.apiKey.View()) + "\n")
	if m.verifMsg != "" {
		b.WriteString("\n" + tui.StatusOKStyle.Render(m.verifMsg))
	}
	b.WriteString("\n" + tui.HelpStyle.Render("[Enter] Verify  [Esc] Back"))
	return b.String()
}

// providerURL returns the key-acquisition URL for a provider.
func providerURL(id string) string {
	switch id {
	case "claude":
		return "https://console.anthropic.com"
	case "deepseek":
		return "https://platform.deepseek.com"
	case "openai":
		return "https://platform.openai.com"
	case "gemini":
		return "https://aistudio.google.com"
	default:
		return ""
	}
}

// Step exposes the current step (for tests).
func (m SetupModel) Step() SetupStep { return m.step }

// SelectedProvider exposes the chosen provider id (for tests).
func (m SetupModel) SelectedProvider() string { return m.provider }

// EchoPassword reports whether the key input is masked (for tests).
func (m SetupModel) EchoPassword() bool { return m.apiKey.EchoMode == textinput.EchoPassword }

const welcomeArt = `╔══════════════════════════════════════╗
║     VORTEX — First Time Setup        ║
║  One binary. Any server. Fully       ║
║  autonomous.                         ║
╚══════════════════════════════════════╝`

const completeArt = `╔══════════════════════════════════════╗
║  ✓ VORTEX configured successfully!   ║
║                                      ║
║  Run: vortex start                   ║
║  Dashboard: localhost:9090/dashboard ║
╚══════════════════════════════════════╝`
