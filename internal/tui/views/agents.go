package views

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	vortexerrors "github.com/vortex-run/vortex/internal/errors"
	"github.com/vortex-run/vortex/internal/tui"
	"github.com/vortex-run/vortex/internal/tui/brand"
)

// ChatMessage is one line in the agent conversation.
type ChatMessage struct {
	Role      string // "user" | "agent" | "system"
	Content   string
	Timestamp time.Time
	JobID     string        // set when a forge build job started
	Took      time.Duration // agent replies: time from submit to response
}

// AgentsModel is the interactive chat with the VORTEX coordinator.
type AgentsModel struct {
	client         *tui.Client
	messages       []ChatMessage
	input          textinput.Model
	viewport       viewport.Model
	spinner        spinner.Model
	thinking       bool
	sessionID      string
	awaiting       bool                // an approval is pending (awaiting Y/N then Enter)
	approvalID     string              // session the pending approval belongs to
	approvalReady  bool                // box has rendered a frame; Y/N now accepted
	approvalChoice string              // staged choice: "" | "approve" | "reject"
	forgeJob       string              // active forge job id being polled ("" = none)
	forgeSeen      int                 // count of progress-history lines already shown
	pendingQs      []tui.ForgeQuestion // clarifying questions awaiting an answer
	thinkingSince  time.Time           // when the in-flight submit started
	// streamText accumulates the in-flight streamed reply (rendered live in
	// place of the Thinking box); streamCh identifies the active stream so a
	// stale stream can never write into the buffer.
	streamText string
	streamCh   <-chan string
	width      int
	height     int
}

// slashCommands are the Claude-Code-style quick commands shown when the input
// begins with "/". Each maps to an agent action.
var slashCommands = []string{
	"/ls", "/read", "/run", "/create", "/edit", "/project",
	"/forge", "/status", "/reload", "/help",
	"/diff", "/commit", "/search", "/find",
	"/history", "/resume", "/undo",
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
		client:   client,
		input:    in,
		viewport: vp,
		spinner:  tui.Spinner(),
		// Generate ONCE; the same session id is used for every Submit/Approve so
		// multi-turn flows (clarifying questions) stay in one session.
		sessionID: fmt.Sprintf("tui-%d", time.Now().UnixMilli()),
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

// agentStreamMsg carries an opened reply stream (AGUI item C); err is set
// when the stream could not be opened.
type agentStreamMsg struct {
	ch  <-chan string
	err error
}

// agentChunkMsg carries one streamed reply fragment, or the end of the stream.
type agentChunkMsg struct {
	ch    <-chan string
	chunk string
	done  bool
}

// readAgentStream waits for the next streamed reply fragment (one per Update).
func readAgentStream(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		chunk, ok := <-ch
		if !ok {
			return agentChunkMsg{ch: ch, done: true}
		}
		return agentChunkMsg{ch: ch, chunk: chunk}
	}
}

// forgeProgress carries a poll of a forge job's status.
type forgeProgress struct {
	job *tui.ForgeJobData
	err error
}

// jobIDPattern extracts a forge job id from a reply line like "Job ID: job-abc".
var jobIDPattern = regexp.MustCompile(`Job ID:\s*(job-[A-Za-z0-9_-]+|[A-Za-z0-9]{8,})`)

// approvalMarker matches the coordinator's approval line, optionally carrying
// "|<risk>|<tool>" metadata: "[APPROVAL_REQUIRED|HIGH RISK|run_terminal] ...".
var approvalMarker = regexp.MustCompile(`\[APPROVAL_REQUIRED(?:\|([^|]*)\|([^\]]*))?\]`)

// riskHeaders maps a risk level to its approval-box header (escalating warnings).
var riskHeaders = map[string]string{
	brand.RiskLow:      "VORTEX wants to make a change",
	brand.RiskMedium:   brand.IconWarn + " VORTEX wants to run a command",
	brand.RiskHigh:     brand.IconWarn + brand.IconWarn + " Review carefully before approving",
	brand.RiskCritical: brand.IconWarn + brand.IconWarn + brand.IconWarn + " This action cannot be undone",
}

// formatApproval rewrites the coordinator's approval marker line into a
// human header plus a risk badge, keying the header severity off the risk
// level the coordinator encoded.
func formatApproval(content string) string {
	risk := brand.RiskMedium
	if m := approvalMarker.FindStringSubmatch(content); m != nil && m[1] != "" {
		risk = m[1]
	}
	header := riskHeaders[risk]
	if header == "" {
		header = brand.IconWarn + " Agent wants approval"
	}
	// Replace the marker with "Agent wants approval — <header>  [BADGE]". The
	// "Agent wants approval" phrase keeps renderMessages routing this into the
	// amber Approval-Required frame.
	replacement := "Agent wants approval — " + header + "  " + brand.RiskBadge(risk)
	return approvalMarker.ReplaceAllString(content, replacement)
}

// extractJobID returns the forge job id mentioned in s, or "".
func extractJobID(s string) string {
	if m := jobIDPattern.FindStringSubmatch(s); len(m) == 2 {
		return m[1]
	}
	return ""
}

// orderTranscript splits a result transcript into non-empty lines, returning
// the command output first and any completion/summary line (✓/✗/Completed/
// File created) last — so the chat reads stdout-then-result.
func orderTranscript(result string) []string {
	var output, summary []string
	for _, line := range strings.Split(result, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if isSummaryLine(line) {
			summary = append(summary, line)
		} else {
			output = append(output, line)
		}
	}
	return append(output, summary...)
}

// isSummaryLine reports whether a transcript line is a completion/summary marker
// (rendered after the command output).
func isSummaryLine(line string) bool {
	l := strings.TrimSpace(line)
	return strings.HasPrefix(l, "✓") || strings.HasPrefix(l, "✗") ||
		strings.HasPrefix(l, "⚠") || strings.Contains(l, "Completed (exit")
}

// pollForge schedules a single status poll of the active forge job after 2s.
func (m AgentsModel) pollForge() tea.Cmd {
	c := m.client
	job := m.forgeJob
	if c == nil || job == "" {
		return nil
	}
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		d, err := c.ForgeStatus(job)
		return forgeProgress{job: d, err: err}
	})
}

// Init starts the spinner ticking.
func (m AgentsModel) Init() tea.Cmd { return m.spinner.Tick }

// submit sends the current input to the coordinator, streaming the reply.
func (m AgentsModel) submit(text string) tea.Cmd {
	c := m.client
	sid := m.sessionID
	return func() tea.Msg {
		if c == nil {
			return agentResponse{err: fmt.Errorf("not connected")}
		}
		ch, err := c.SubmitStream(text, sid)
		if err != nil {
			return agentResponse{err: err}
		}
		return agentStreamMsg{ch: ch}
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
		// While waiting on the agent, rebuild the transcript each frame so the
		// "⠸ Thinking…" line actually animates (reassures the user the request
		// is in flight). Keep the view pinned to the bottom.
		if m.thinking {
			m.viewport.SetContent(m.renderMessages())
			m.viewport.GotoBottom()
		}
		return m, cmd

	case agentStreamMsg:
		if msg.err != nil {
			return m.Update(agentResponse{err: msg.err})
		}
		m.streamText = ""
		m.streamCh = msg.ch
		return m, readAgentStream(msg.ch)

	case agentChunkMsg:
		if msg.ch != m.streamCh {
			// Stale stream (submits are blocked while thinking, so this is
			// belt-and-braces): drain silently without touching the buffer.
			if msg.done {
				return m, nil
			}
			return m, readAgentStream(msg.ch)
		}
		if msg.done {
			full := m.streamText
			m.streamText = ""
			m.streamCh = nil
			// Route the accumulated reply through the normal handling so
			// approvals, forge-job detection, and timing behave unchanged.
			return m.Update(agentResponse{content: full})
		}
		m.streamText += msg.chunk
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, readAgentStream(msg.ch)

	case agentResponse:
		m.thinking = false
		took := time.Duration(0)
		if !m.thinkingSince.IsZero() {
			took = time.Since(m.thinkingSince)
			m.thinkingSince = time.Time{}
		}
		content := msg.content
		if msg.err != nil {
			// Surface a friendly, actionable explanation (rate limit → add backup
			// keys, invalid key → run setup, etc.) instead of the raw error.
			content = vortexerrors.NewFriendly(msg.err).Short()
		}
		// An [APPROVAL_REQUIRED...] line means the agent is waiting on a decision.
		if approvalMarker.MatchString(content) {
			m.awaiting = true
			m.approvalReady = false // ignore keys until the box has rendered a frame
			m.approvalChoice = ""
			m.approvalID = m.sessionID
			content = formatApproval(content)
			content += "\n\nPress [Y] then Enter to approve, or [N] then Enter to reject."
			m.messages = append(m.messages, ChatMessage{Role: "agent", Content: content, Timestamp: time.Now()})
			m.viewport.SetContent(m.renderMessages())
			m.viewport.GotoBottom()
			m.input.Focus()
			// Enable Y/N only after a brief delay, so a stray keypress in flight
			// cannot resolve the approval before the user sees the box.
			return m, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return approvalReadyMsg{} })
		}
		m.messages = append(m.messages, ChatMessage{Role: "agent", Content: content, Timestamp: time.Now(), Took: took})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		m.input.Focus()
		// If the reply started a forge build, begin polling its progress.
		if id := extractJobID(content); id != "" {
			m.forgeJob = id
			m.forgeSeen = 0
			return m, m.pollForge()
		}
		return m, nil

	case approvalReadyMsg:
		m.approvalReady = true
		return m, nil

	case historyListMsg:
		if msg.err != nil {
			m.messages = append(m.messages, ChatMessage{Role: "system", Content: "⚠ " + msg.err.Error(), Timestamp: time.Now()})
		} else if len(msg.sessions) == 0 {
			m.messages = append(m.messages, ChatMessage{Role: "system", Content: "No past sessions.", Timestamp: time.Now()})
		} else {
			m.messages = append(m.messages, ChatMessage{Role: "system", Content: "Past sessions (use /resume <id>):", Timestamp: time.Now()})
			for _, s := range msg.sessions {
				m.messages = append(m.messages, ChatMessage{Role: "system", Content: "• " + s.SessionID + " — " + s.Summary, Timestamp: time.Now()})
			}
		}
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil

	case resumeMsg:
		if msg.err != nil {
			m.messages = append(m.messages, ChatMessage{Role: "system", Content: "⚠ " + msg.err.Error(), Timestamp: time.Now()})
		} else {
			m.messages = append(m.messages, ChatMessage{Role: "system", Content: "Resumed session " + msg.sessionID + ":", Timestamp: time.Now()})
			for _, hm := range msg.messages {
				m.messages = append(m.messages, ChatMessage{Role: hm.Role, Content: hm.Content, Timestamp: time.Now()})
			}
		}
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil

	case forgeProgress:
		return m.handleForgeProgress(msg)

	case approvalResult:
		m.awaiting = false
		m.approvalReady = false
		m.approvalChoice = ""
		if msg.approved {
			m.messages = append(m.messages, ChatMessage{Role: "system", Content: "✓ Action approved", Timestamp: time.Now()})
		} else {
			m.messages = append(m.messages, ChatMessage{Role: "system", Content: "✗ Action rejected", Timestamp: time.Now()})
		}
		// Show the server-side execution transcript line by line, with the
		// completion/summary line LAST: command stdout/stderr appear first, then
		// "✓ Completed (exit N)" — so the chat reads output-then-result.
		if strings.TrimSpace(msg.result) != "" {
			for _, line := range orderTranscript(msg.result) {
				m.messages = append(m.messages, ChatMessage{Role: "agent", Content: line, Timestamp: time.Now()})
			}
		}
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil

	case tea.KeyMsg:
		// While an approval is pending: Y/N STAGE a choice, Enter CONFIRMS it.
		// Keys are ignored until the box has had a frame to render (approvalReady),
		// preventing an in-flight keypress from resolving it prematurely.
		if m.awaiting {
			if !m.approvalReady {
				return m, nil
			}
			switch strings.ToLower(msg.String()) {
			case "y":
				m.approvalChoice = "approve"
				m.viewport.SetContent(m.renderMessages())
				return m, nil
			case "n":
				m.approvalChoice = "reject"
				m.viewport.SetContent(m.renderMessages())
				return m, nil
			case "enter":
				switch m.approvalChoice {
				case "approve":
					return m, m.sendApproval(true)
				case "reject":
					return m, m.sendApproval(false)
				}
				return m, nil // Enter with no staged choice: ignore
			}
			return m, nil
		}
		switch msg.String() {
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" || m.thinking {
				return m, nil
			}
			// /history and /resume are handled client-side (query the memory
			// store via the API) instead of being sent to the coordinator.
			if cmd, handled := m.handleHistoryCommand(text); handled {
				m.input.Reset()
				m.viewport.SetContent(m.renderMessages())
				m.viewport.GotoBottom()
				return m, cmd
			}
			// If clarifying questions are pending, map a "2 1" answer to the
			// selected option texts (or pass free text through) and submit it.
			submitText := text
			if len(m.pendingQs) > 0 {
				mapped, _ := parseOptionAnswer(text, m.pendingQs)
				submitText = mapped
				m.pendingQs = nil
			}
			m.messages = append(m.messages, ChatMessage{Role: "user", Content: text, Timestamp: time.Now()})
			m.input.Reset()
			m.thinking = true
			m.thinkingSince = time.Now()
			m.viewport.SetContent(m.renderMessages())
			m.viewport.GotoBottom()
			return m, tea.Batch(m.submit(submitText), m.spinner.Tick)
		case "ctrl+l":
			m.messages = m.messages[:1] // keep the system greeting
			m.viewport.SetContent(m.renderMessages())
			return m, nil
		case "ctrl+c":
			m.input.Reset()
			return m, nil
		case "tab":
			// Tab autocompletes commands — but NOT while answering option
			// questions (there the user types numbers, not commands).
			if len(m.pendingQs) == 0 {
				m.input.SetValue(autocomplete(m.input.Value()))
				m.input.CursorEnd()
			}
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

	// While answering option questions, show a clear numbered-entry prompt.
	if len(m.pendingQs) > 0 {
		m.input.Prompt = "Enter numbers (e.g. 1 2): "
	}
	footer := m.input.View()
	if m.thinking {
		footer = m.spinner.View() + " Thinking…"
	}

	return strings.Join([]string{header, "", body, "", footer}, "\n")
}

// renderMessages renders the full conversation as brand message bubbles:
// user messages right-aligned, agent replies left-aligned with a VORTEX (or
// Approval Required) frame, system notices as centered separators.
func (m AgentsModel) renderMessages() string {
	w := m.width
	if w < 30 {
		w = 60
	}
	var b strings.Builder
	for _, msg := range m.messages {
		switch msg.Role {
		case "user":
			box := titledBox("You", msg.Content, brand.ColorPrimary, minInt(w-2, 70))
			b.WriteString(lipgloss.PlaceHorizontal(w, lipgloss.Right, box) + "\n\n")
		case "agent":
			title, color := "VORTEX", brand.ColorBorder
			if strings.Contains(msg.Content, "Agent wants approval") {
				title, color = "Approval Required", brand.ColorWarning
			}
			b.WriteString(titledBox(title, msg.Content, color, minInt(w-2, 90)) + "\n")
			if msg.Took > 0 {
				b.WriteString(brand.StyleSubtitle.Render(fmt.Sprintf("─ %.1fs ─", msg.Took.Seconds())) + "\n")
			}
			b.WriteString("\n")
		default:
			b.WriteString(brand.StyleSystemMsg.Render("─── "+msg.Content+" ───") + "\n\n")
		}
	}
	if m.thinking {
		// Once tokens arrive, the reply streams into the box live with the
		// spinner as its cursor; before that, the Thinking indicator.
		body := m.spinner.View() + " Thinking…"
		if m.streamText != "" {
			body = m.streamText + " " + m.spinner.View()
		}
		b.WriteString(titledBox("VORTEX", body, brand.ColorBorder, minInt(w-2, 90)))
	}
	// Show the staged approval choice so the user sees it before confirming.
	if m.awaiting && m.approvalChoice != "" {
		label := "▶ Approve selected — press Enter to confirm"
		if m.approvalChoice == "reject" {
			label = "▶ Reject selected — press Enter to confirm"
		}
		b.WriteString(brand.StyleWarn.Render(label))
	}
	return b.String()
}

// titledBox draws a "┌─ Title ────┐" frame around content in the given border
// color, wrapping the body to maxWidth. Measurements are ANSI-aware.
func titledBox(title, content string, borderColor string, maxWidth int) string {
	if maxWidth < 12 {
		maxWidth = 12
	}
	cs := lipgloss.NewStyle().Foreground(lipgloss.Color(borderColor))
	body := lipgloss.NewStyle().Width(maxWidth - 4).Render(content)
	lines := strings.Split(body, "\n")
	w := lipgloss.Width(title) + 4
	for _, l := range lines {
		if lw := lipgloss.Width(l); lw > w {
			w = lw
		}
	}
	var b strings.Builder
	b.WriteString(cs.Render("┌─ "+title+" "+strings.Repeat("─", maxInt0(w-lipgloss.Width(title)-1))+"┐") + "\n")
	for _, l := range lines {
		b.WriteString(cs.Render("│") + " " + l +
			strings.Repeat(" ", maxInt0(w+1-lipgloss.Width(l))) + cs.Render("│") + "\n")
	}
	b.WriteString(cs.Render("└" + strings.Repeat("─", w+2) + "┘"))
	return b.String()
}

// maxInt0 clamps n to >= 0 (repeat counts must not be negative).
func maxInt0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// minInt returns the smaller of a and b.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// handleForgeProgress appends any new progress lines and keeps polling until the
// job reaches a terminal state.
func (m AgentsModel) handleForgeProgress(msg forgeProgress) (tea.Model, tea.Cmd) {
	if msg.err != nil || msg.job == nil {
		m.forgeJob = "" // stop polling on error
		return m, nil
	}
	// Append only history lines we haven't shown yet.
	for i := m.forgeSeen; i < len(msg.job.ProgressHistory); i++ {
		m.messages = append(m.messages, ChatMessage{
			Role: "agent", Content: "⠸ " + msg.job.ProgressHistory[i], Timestamp: time.Now(), JobID: msg.job.ID,
		})
	}
	m.forgeSeen = len(msg.job.ProgressHistory)

	switch msg.job.State {
	case "complete":
		summary := msg.job.Result
		if summary == "" {
			summary = "Build complete"
		}
		if msg.job.DurationMs > 0 {
			summary += fmt.Sprintf(" (%.1fs)", float64(msg.job.DurationMs)/1000)
		}
		m.messages = append(m.messages, ChatMessage{Role: "agent", Content: "✓ " + summary, Timestamp: time.Now()})
		m.forgeJob = ""
	case "failed":
		m.messages = append(m.messages, ChatMessage{Role: "agent", Content: "✗ Build failed: " + msg.job.Error, Timestamp: time.Now()})
		m.forgeJob = ""
	case "needs_clarification":
		if len(msg.job.Questions) > 0 {
			m.pendingQs = msg.job.Questions
			m.messages = append(m.messages, ChatMessage{Role: "agent", Content: renderQuestions(msg.job.Questions), Timestamp: time.Now()})
			m.forgeJob = "" // stop polling; wait for the user's answer
		}
	}

	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
	if m.forgeJob != "" {
		return m, m.pollForge() // keep polling
	}
	return m, nil
}

// ForgePolling reports whether a forge job is being polled (for tests).
func (m AgentsModel) ForgePolling() bool { return m.forgeJob != "" }

// PendingQuestions reports whether option-selection questions are awaiting an
// answer (for tests).
func (m AgentsModel) PendingQuestions() bool { return len(m.pendingQs) > 0 }

// renderQuestions renders structured clarifying questions as a numbered
// selection block (Claude-Code style).
func renderQuestions(qs []tui.ForgeQuestion) string {
	var b strings.Builder
	b.WriteString("Before I build, a couple of quick questions:\n")
	for i, q := range qs {
		b.WriteString(fmt.Sprintf("\n%d. %s\n", i+1, q.Question))
		for j, opt := range q.Options {
			b.WriteString(fmt.Sprintf("   [%d] %s\n", j+1, opt))
		}
	}
	b.WriteString("\nType numbers separated by space (e.g. \"2 1\"), or describe freely.")
	return b.String()
}

// parseOptionAnswer maps a "2 1"-style answer to the selected option texts. If
// the input isn't all numbers, it returns the raw text (free-text fallback) and
// freeText=true.
func parseOptionAnswer(input string, qs []tui.ForgeQuestion) (answer string, freeText bool) {
	fields := strings.Fields(strings.TrimSpace(input))
	// Every field must be a valid option index for the structured path.
	if len(fields) == 0 || len(fields) > len(qs) {
		return input, true
	}
	var picks []string
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > len(qs[i].Options) {
			return input, true // not a clean numeric selection → free text
		}
		picks = append(picks, qs[i].Options[n-1])
	}
	return strings.Join(picks, ", "), false
}

// approvalResult is emitted after an approve/reject decision is sent. result is
// the server-side execution transcript (the file write happens on approval).
type approvalResult struct {
	approved bool
	result   string
}

// approvalReadyMsg fires ~100ms after an approval box renders, enabling Y/N.
type approvalReadyMsg struct{}

// historyListMsg carries the list of past sessions for /history.
type historyListMsg struct {
	sessions []tui.SessionSummaryData
	err      error
}

// resumeMsg carries a resumed session's messages for /resume.
type resumeMsg struct {
	sessionID string
	messages  []tui.SessionMessageData
	err       error
}

// handleHistoryCommand intercepts /history and /resume. It returns (cmd, true)
// when it handled the input, else (nil, false) so normal submit proceeds.
func (m *AgentsModel) handleHistoryCommand(text string) (tea.Cmd, bool) {
	switch {
	case text == "/history":
		c := m.client
		return func() tea.Msg {
			if c == nil {
				return historyListMsg{err: fmt.Errorf("not connected")}
			}
			s, err := c.History()
			return historyListMsg{sessions: s, err: err}
		}, true
	case strings.HasPrefix(text, "/resume "):
		id := strings.TrimSpace(strings.TrimPrefix(text, "/resume "))
		c := m.client
		return func() tea.Msg {
			if c == nil {
				return resumeMsg{err: fmt.Errorf("not connected")}
			}
			msgs, err := c.SessionHistory(id)
			return resumeMsg{sessionID: id, messages: msgs, err: err}
		}, true
	}
	return nil, false
}

// ApprovalReady reports whether Y/N input is enabled on the box (for tests).
func (m AgentsModel) ApprovalReady() bool { return m.approvalReady }

// ApprovalChoice returns the staged choice ("approve"/"reject"/"") (for tests).
func (m AgentsModel) ApprovalChoice() string { return m.approvalChoice }

// sendApproval posts the user's approve/reject decision to the agent runtime
// and carries back the result transcript (the action executes on approval).
func (m AgentsModel) sendApproval(approved bool) tea.Cmd {
	c := m.client
	sid := m.approvalID
	return func() tea.Msg {
		result := ""
		if c != nil {
			result, _ = c.Approve(sid, approved)
		}
		return approvalResult{approved: approved, result: result}
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

// IsInputFocused always returns true: the Agents chat input is active at all
// times (including approval Y/N and option-selection), so the app must not
// intercept q/Tab/1-9 navigation while the user is here — every key is typed
// into the chat or consumed by the view.
func (m AgentsModel) IsInputFocused() bool { return true }

// Messages exposes the conversation (for tests).
func (m AgentsModel) Messages() []ChatMessage { return m.messages }

// Thinking reports whether the agent is currently processing (for tests).
func (m AgentsModel) Thinking() bool { return m.thinking }
