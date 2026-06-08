package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
)

// ChatMessage is one line in the agent conversation.
type ChatMessage struct {
	Role      string // "user" | "agent" | "system"
	Content   string
	Timestamp time.Time
	JobID     string // set when a forge build job started
}

// AgentsModel is the interactive chat with the VORTEX coordinator.
type AgentsModel struct {
	client     *tui.Client
	messages   []ChatMessage
	input      textinput.Model
	viewport   viewport.Model
	spinner    spinner.Model
	thinking   bool
	sessionID  string
	awaiting   bool   // an approval is pending (awaiting Y/N)
	approvalID string // session the pending approval belongs to
	width      int
	height     int
}

// slashCommands are the Claude-Code-style quick commands shown when the input
// begins with "/". Each maps to an agent action.
var slashCommands = []string{
	"/ls", "/read", "/run", "/create", "/edit", "/project",
	"/forge", "/status", "/reload", "/help",
}

// commandCompletions are tab-completed command prefixes.
var commandCompletions = []string{
	"build me a ", "research ", "status", "show routes", "reload config", "show logs",
}

// NewAgents constructs the chat model.
func NewAgents(client *tui.Client) AgentsModel {
	in := textinput.New()
	in.Placeholder = "Type a message…"
	in.Prompt = "> "
	in.Focus()

	vp := viewport.New(0, 0)

	return AgentsModel{
		client:    client,
		input:     in,
		viewport:  vp,
		spinner:   tui.Spinner(),
		sessionID: fmt.Sprintf("tui-%d", time.Now().Unix()),
		messages: []ChatMessage{
			{Role: "system", Content: "Agent runtime ready. How can I help?", Timestamp: time.Now()},
		},
	}
}

// agentResponse carries a coordinator reply (or error).
type agentResponse struct {
	content string
	err     error
}

// Init starts the spinner ticking.
func (m AgentsModel) Init() tea.Cmd { return m.spinner.Tick }

// submit sends the current input to the coordinator.
func (m AgentsModel) submit(text string) tea.Cmd {
	c := m.client
	sid := m.sessionID
	return func() tea.Msg {
		if c == nil {
			return agentResponse{err: fmt.Errorf("not connected")}
		}
		resp, err := c.Submit(text, sid)
		return agentResponse{content: resp, err: err}
	}
}

// Update handles input, sending, and responses.
func (m AgentsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = msg.Width - 4
		m.viewport.Width = msg.Width
		m.viewport.Height = max(msg.Height-6, 1)
		m.viewport.SetContent(m.renderMessages())

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case agentResponse:
		m.thinking = false
		content := msg.content
		if msg.err != nil {
			content = "⚠ " + msg.err.Error()
		}
		// An [APPROVAL_REQUIRED] line means the agent is waiting on a Y/N.
		if strings.Contains(content, "[APPROVAL_REQUIRED]") {
			m.awaiting = true
			m.approvalID = m.sessionID
			content = strings.ReplaceAll(content, "[APPROVAL_REQUIRED]", "⚠ Agent wants approval —")
			content += "\n\n[Y] Approve    [N] Reject"
		}
		m.messages = append(m.messages, ChatMessage{Role: "agent", Content: content, Timestamp: time.Now()})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		m.input.Focus()
		return m, nil

	case approvalResult:
		m.awaiting = false
		verb := "rejected"
		if msg.approved {
			verb = "approved"
		}
		m.messages = append(m.messages, ChatMessage{Role: "system", Content: "Action " + verb + ".", Timestamp: time.Now()})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil

	case tea.KeyMsg:
		// While an approval is pending, Y/N resolve it (and nothing else types).
		if m.awaiting {
			switch strings.ToLower(msg.String()) {
			case "y":
				return m, m.sendApproval(true)
			case "n":
				return m, m.sendApproval(false)
			}
			return m, nil
		}
		switch msg.String() {
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" || m.thinking {
				return m, nil
			}
			m.messages = append(m.messages, ChatMessage{Role: "user", Content: text, Timestamp: time.Now()})
			m.input.Reset()
			m.thinking = true
			m.viewport.SetContent(m.renderMessages())
			m.viewport.GotoBottom()
			return m, tea.Batch(m.submit(text), m.spinner.Tick)
		case "ctrl+l":
			m.messages = m.messages[:1] // keep the system greeting
			m.viewport.SetContent(m.renderMessages())
			return m, nil
		case "ctrl+c":
			m.input.Reset()
			return m, nil
		case "tab":
			m.input.SetValue(autocomplete(m.input.Value()))
			m.input.CursorEnd()
			return m, nil
		}
	}

	// Forward remaining keys to the input + viewport.
	var icmd, vcmd tea.Cmd
	m.input, icmd = m.input.Update(msg)
	m.viewport, vcmd = m.viewport.Update(msg)
	cmds = append(cmds, icmd, vcmd)
	return m, tea.Batch(cmds...)
}

// View renders the chat screen.
func (m AgentsModel) View() string {
	header := tui.TitleStyle.Render("VORTEX Agent") + "  " +
		tui.StatusDot(m.client != nil) + " " +
		tui.SubtitleStyle.Render("Session: "+m.sessionID)

	body := m.viewport.View()
	if body == "" {
		body = m.renderMessages()
	}

	footer := m.input.View()
	if m.thinking {
		footer = m.spinner.View() + " Thinking…"
	}

	return strings.Join([]string{header, "", body, "", footer}, "\n")
}

// renderMessages renders the full conversation with role-based styling.
func (m AgentsModel) renderMessages() string {
	var b strings.Builder
	for _, msg := range m.messages {
		switch msg.Role {
		case "user":
			b.WriteString(tui.ChatUserStyle.Render("[user] "+msg.Content) + "\n\n")
		case "agent":
			b.WriteString(tui.ChatAgentStyle.Render("[agent] "+msg.Content) + "\n\n")
		default:
			b.WriteString(tui.ChatSystemStyle.Render("[system] "+msg.Content) + "\n\n")
		}
	}
	if m.thinking {
		b.WriteString(tui.ChatAgentStyle.Render("[agent] " + m.spinner.View() + " Thinking…"))
	}
	return b.String()
}

// approvalResult is emitted after an approve/reject decision is sent.
type approvalResult struct {
	approved bool
}

// sendApproval posts the user's approve/reject decision to the agent runtime.
func (m AgentsModel) sendApproval(approved bool) tea.Cmd {
	c := m.client
	sid := m.approvalID
	return func() tea.Msg {
		if c != nil {
			_ = c.Approve(sid, approved)
		}
		return approvalResult{approved: approved}
	}
}

// autocomplete returns the first command completion matching the current input.
// When the input begins with "/", it cycles the slash commands instead.
func autocomplete(current string) string {
	trimmed := strings.TrimSpace(current)
	if strings.HasPrefix(trimmed, "/") {
		for _, c := range slashCommands {
			if strings.HasPrefix(c, trimmed) && c != trimmed {
				return c + " "
			}
		}
		return trimmed
	}
	if trimmed == "" {
		return commandCompletions[0]
	}
	for _, c := range commandCompletions {
		if strings.HasPrefix(c, trimmed) && c != trimmed {
			return c
		}
	}
	return trimmed
}

// Awaiting reports whether an approval is pending (for tests).
func (m AgentsModel) Awaiting() bool { return m.awaiting }

// Messages exposes the conversation (for tests).
func (m AgentsModel) Messages() []ChatMessage { return m.messages }

// Thinking reports whether the agent is currently processing (for tests).
func (m AgentsModel) Thinking() bool { return m.thinking }
