package views

import (
	"context"
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

	"github.com/vortex-run/vortex/internal/tui"
	"github.com/vortex-run/vortex/internal/tui/brand"
)

// ActivityEntry is one line/box in the code view's activity feed.
type ActivityEntry struct {
	Timestamp time.Time
	Source    string // "user" | "coordinator" | "system" | agent name
	Content   string
	Kind      string // "message"|"step-start"|"step-item"|"step-end"|"error"
	StepNum   int
	StepTotal int
}

// CodeAgentStatus is one specialist agent's state in the left panel.
type CodeAgentStatus struct {
	Name   string
	Role   string
	Status string // "ready"|"busy"|"idle"|"error"
}

// TaskProgress is the live state of the running task.
type TaskProgress struct {
	Current int
	Total   int
	Step    string
	Percent float64
	EstSecs int
	CostUSD float64
}

// codeStatsMsg carries an /api/agents/status + cost refresh.
type codeStatsMsg struct {
	stats   *tui.AgentsData
	cost    *tui.AICostData
	offline bool // the stats fetch could not reach the server
}

// codeReplyMsg carries the coordinator's final reply for a submitted task.
type codeReplyMsg struct {
	content string
	err     error
}

// codeTickMsg drives the periodic panel refresh.
type codeTickMsg time.Time

// codeSidebarWidth is the fixed left panel width.
const codeSidebarWidth = 26

// stepPattern recognises "Step N/M" or "step N of M" markers in agent output
// so the feed can frame them and the progress bar can track them.
var stepPattern = regexp.MustCompile(`(?i)step\s+(\d+)\s*(?:/|of)\s*(\d+)`)

// CodeModel is the dedicated coding interface (brand redesign part 5) — the
// full-screen "vortex code" experience: specialist-agent roster, memory stats,
// task progress, and a live activity feed.
type CodeModel struct {
	client   *tui.Client
	activity []ActivityEntry
	viewport viewport.Model
	input    textinput.Model
	spin     spinner.Model
	agents   []CodeAgentStatus
	progress TaskProgress
	skills   []string // skills surfaced during this session
	stats    *tui.AgentsData
	cost     *tui.AICostData
	// memOffline is set when a stats refresh cannot reach the server, so the
	// MEMORY panel shows "✗ Server offline" instead of a stuck "(connecting...)".
	memOffline bool
	// streamedThisTurn is set while tokens for the current reply stream into the
	// chat panel, so the final codeReplyMsg does not append a duplicate line.
	streamedThisTurn bool

	sessionID   string
	project     string       // project dir shown in the header
	model       string       // AI model override shown in the header
	projectInfo *ProjectInfo // AGENTS.md summary shown in the PROJECT panel
	team        bool         // multi-agent orchestration (default true)
	working     bool         // a task is in flight
	workStart   time.Time
	costAtStart float64
	paused      bool
	confirmStop bool
	helpOpen    bool
	standalone  bool // running as `vortex code` (q quits the program)

	// --- three-panel collaboration state (AG-UI) ---
	comms         []CommsEntry  // middle panel: inter-agent messages
	selectedAgent string        // which agent the right-panel chat targets
	chat          []ChatLine    // right panel: conversation with selectedAgent
	checkpoint    *CheckpointUI // active checkpoint review (nil = none)
	viewingFile   int           // index into checkpoint.Files (-1 = none)

	// commsCh delivers live bus messages from the server's SSE feed; nil until
	// the stream is opened in Init. listenComms re-subscribes after each message.
	commsCh <-chan tui.CommsRecord

	width  int
	height int
}

// CommsEntry is one inter-agent message in the middle panel.
type CommsEntry struct {
	Time    time.Time
	From    string
	To      string
	Kind    string // message|checkpoint|approved|rejected|edited
	Content string
}

// ChatLine is one turn in the right-panel direct chat.
type ChatLine struct {
	Role    string // user|agent
	Agent   string
	Content string
}

// CheckpointUI is the active checkpoint shown in the right panel.
type CheckpointUI struct {
	ID          string
	Title       string
	Description string
	FromAgent   string
	ToAgent     string
	Files       []CheckpointFile
}

// CheckpointFile is one file in a checkpoint review.
type CheckpointFile struct {
	Path    string
	Content string
	Lines   int
	IsNew   bool
}

// CodeOption configures NewCode.
type CodeOption func(*CodeModel)

// WithProject sets the project directory shown in the header.
func WithProject(dir string) CodeOption { return func(m *CodeModel) { m.project = dir } }

// WithModel sets the AI model override shown in the header.
func WithModel(model string) CodeOption { return func(m *CodeModel) { m.model = model } }

// WithoutTeam disables multi-agent orchestration (single-agent mode).
func WithoutTeam() CodeOption { return func(m *CodeModel) { m.team = false } }

// WithTeam forces multi-agent team mode on.
func WithTeam() CodeOption { return func(m *CodeModel) { m.team = true } }

// ProjectInfo is the AGENTS.md summary shown in the PROJECT panel.
type ProjectInfo struct {
	Name    string
	Stack   []string
	TestCmd string
}

// WithProjectInfo sets the AGENTS.md summary shown in the left panel.
func WithProjectInfo(info *ProjectInfo) CodeOption {
	return func(m *CodeModel) { m.projectInfo = info }
}

// Standalone marks the model as the root of its own program (`vortex code`):
// q quits the program instead of being typed.
func Standalone() CodeOption { return func(m *CodeModel) { m.standalone = true } }

// CommsMsg feeds an inter-agent message into the middle panel.
type CommsMsg CommsEntry

// CheckpointMsg activates checkpoint review in the right panel.
type CheckpointMsg CheckpointUI

// DirectReplyMsg delivers an agent's direct-chat reply to the right panel.
type DirectReplyMsg struct {
	Agent   string
	Content string
}

// NewCode constructs the coding interface model.
func NewCode(client *tui.Client, opts ...CodeOption) CodeModel {
	in := textinput.New()
	in.Placeholder = "Type to interrupt, ask a question, or give new instructions..."
	in.Prompt = "> "
	in.Focus()

	m := CodeModel{
		client:        client,
		input:         in,
		viewport:      viewport.New(0, 0),
		spin:          tui.Spinner(),
		team:          true,
		selectedAgent: "coordinator",
		viewingFile:   -1,
		sessionID:     fmt.Sprintf("code-%d", time.Now().UnixMilli()),
		agents: []CodeAgentStatus{
			{Name: "Coordinator", Role: "plans + routes", Status: "ready"},
			{Name: "Code Agent", Role: "writes code", Status: "ready"},
			{Name: "Test Agent", Role: "runs tests", Status: "idle"},
			{Name: "Review", Role: "reviews diffs", Status: "idle"},
			{Name: "DevOps", Role: "deploys", Status: "idle"},
		},
	}
	for _, opt := range opts {
		opt(&m)
	}
	m.addEntry("system", "Session started", "message")
	return m
}

// agentIDByIndex maps the 1-4 selection keys to agent ids.
var agentIDByIndex = []string{"coordinator", "code-agent", "test-agent", "review-agent"}

// selectAgentName returns the display name of the currently selected chat agent.
func (m CodeModel) selectAgentName() string {
	switch m.selectedAgent {
	case "coordinator":
		return "Coordinator"
	case "code-agent":
		return "Code Agent"
	case "test-agent":
		return "Test Agent"
	case "review-agent":
		return "Review Agent"
	default:
		return m.selectedAgent
	}
}

// codeTick schedules the next stats refresh.
func codeTick() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return codeTickMsg(t) })
}

// Init starts the spinner, the stats ticker, and (in team mode) the live
// agent-communication stream.
func (m CodeModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spin.Tick, m.fetchStats(), codeTick()}
	if m.team {
		cmds = append(cmds, m.startCommsStream())
	}
	return tea.Batch(cmds...)
}

// commsStreamReadyMsg carries the opened SSE channel back into the model.
type commsStreamReadyMsg struct{ ch <-chan tui.CommsRecord }

// startCommsStream opens the agent-communication SSE feed. On failure it returns
// a nil-channel ready msg; the codeTick refresh retries by re-issuing it.
func (m CodeModel) startCommsStream() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		if c == nil {
			return commsStreamReadyMsg{}
		}
		ch, err := c.StreamComms(context.Background())
		if err != nil {
			return commsStreamReadyMsg{}
		}
		return commsStreamReadyMsg{ch: ch}
	}
}

// listenComms blocks on the live comms channel and returns the next message as a
// CommsMsg. It re-issues itself after each message (see the CommsMsg handler) so
// the stream keeps flowing; when the channel closes it returns a sentinel that
// triggers a reconnect on the next tick.
func (m CodeModel) listenComms() tea.Cmd {
	ch := m.commsCh
	return func() tea.Msg {
		if ch == nil {
			return nil
		}
		rec, ok := <-ch
		if !ok {
			return commsClosedMsg{}
		}
		return CommsMsg{
			Time:    time.Now(),
			From:    rec.From,
			To:      rec.To,
			Kind:    commsKind(rec.Type),
			Content: rec.Content,
		}
	}
}

// commsClosedMsg signals the comms stream ended so it can be reopened.
type commsClosedMsg struct{}

// commsKind maps a bus message type to the comms panel's kind tag. Checkpoint
// keeps its special rendering; task/result/progress are passed through so the
// roster-status logic can tell a hand-off from a result. Anything else renders
// as a normal message line.
func commsKind(busType string) string {
	switch busType {
	case "checkpoint", "task", "result", "progress":
		return busType
	default:
		return "message"
	}
}

// fetchStats loads agent runtime stats + AI cost.
func (m CodeModel) fetchStats() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		out := codeStatsMsg{}
		if c == nil {
			out.offline = true
			return out
		}
		if s, err := c.Agents(); err == nil {
			out.stats = s
		} else {
			out.offline = true
		}
		if cost, err := c.AICost(); err == nil {
			out.cost = cost
		}
		return out
	}
}

// submit sends a task to the coordinator (orchestrated when team mode is on).
func (m CodeModel) submit(text string) tea.Cmd {
	c := m.client
	sid := m.sessionID
	goal := text
	if m.team && !strings.HasPrefix(text, "/") {
		goal = "/orchestrate " + text
	}
	return func() tea.Msg {
		if c == nil {
			return codeReplyMsg{err: fmt.Errorf("not connected — run: vortex start")}
		}
		resp, err := c.Submit(goal, sid)
		return codeReplyMsg{content: resp, err: err}
	}
}

// addEntry appends one activity entry, stamping the time.
func (m *CodeModel) addEntry(source, content, kind string) {
	e := ActivityEntry{Timestamp: time.Now(), Source: source, Content: content, Kind: kind}
	if mm := stepPattern.FindStringSubmatch(content); mm != nil {
		e.StepNum = atoiSafe(mm[1])
		e.StepTotal = atoiSafe(mm[2])
	}
	m.activity = append(m.activity, e)
}

// atoiSafe parses n, returning 0 on failure (regex already guarantees digits).
func atoiSafe(n string) int {
	v, err := strconv.Atoi(n)
	if err != nil {
		return 0
	}
	return v
}

// ingestReply splits a coordinator reply into feed entries, framing step
// markers and tracking progress / skills mentioned.
func (m *CodeModel) ingestReply(content string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kind := "message"
		switch {
		case stepPattern.MatchString(line):
			kind = "step-start"
		case strings.HasPrefix(line, "✓"):
			kind = "step-end"
		case strings.HasPrefix(line, "✗") || strings.HasPrefix(line, "⚠"):
			kind = "error"
		case strings.HasPrefix(line, "›") || strings.HasPrefix(line, "•") || strings.HasPrefix(line, "-"):
			kind = "step-item"
		}
		m.addEntry("coordinator", line, kind)
		if mm := stepPattern.FindStringSubmatch(line); mm != nil {
			m.progress.Current = atoiSafe(mm[1])
			m.progress.Total = atoiSafe(mm[2])
			m.progress.Step = line
			if m.progress.Total > 0 {
				m.progress.Percent = float64(m.progress.Current) / float64(m.progress.Total) * 100
			}
		}
		if low := strings.ToLower(line); strings.Contains(low, "skill") {
			m.skills = appendUnique(m.skills, strings.TrimSpace(line))
		}
	}
}

// appendUnique appends s if absent, capping the list at 5.
func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	if len(list) >= 5 {
		return list
	}
	return append(list, s)
}

// Update handles keys, replies, and refreshes.
func (m CodeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Live comms-stream lifecycle (team mode). Handled before HandleAGUI so the
	// listen loop keeps re-subscribing.
	switch lm := msg.(type) {
	case commsStreamReadyMsg:
		m.commsCh = lm.ch
		if lm.ch == nil {
			return m, nil // stream not available yet; codeTick retries
		}
		return m, m.listenComms()
	case commsClosedMsg:
		m.commsCh = nil
		return m, nil // a later codeTick reopens the stream
	}

	// Three-panel collaboration messages + selection/checkpoint keys are handled
	// first; if consumed, the standard handling is skipped. A live CommsMsg must
	// re-arm the listen loop so subsequent bus messages keep arriving.
	if updated, handled := m.HandleAGUI(msg); handled {
		if _, isComms := msg.(CommsMsg); isComms {
			return updated, updated.listenComms()
		}
		return updated, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = msg.Width - 6
		m.viewport.Width = maxInt2(msg.Width-codeSidebarWidth-3, 20)
		m.viewport.Height = maxInt2(msg.Height-6, 5)
		m.viewport.SetContent(m.renderFeed())
		return m, nil

	case codeTickMsg:
		cmds := []tea.Cmd{codeTick()}
		if !m.paused {
			cmds = append(cmds, m.fetchStats())
		}
		// Reopen the comms stream if it was never established or has closed.
		if m.team && m.commsCh == nil {
			cmds = append(cmds, m.startCommsStream())
		}
		return m, tea.Batch(cmds...)

	case codeStatsMsg:
		m.memOffline = msg.offline
		if msg.stats != nil {
			m.stats = msg.stats
		}
		if msg.cost != nil {
			m.cost = msg.cost
			if m.working {
				m.progress.CostUSD = msg.cost.TotalUSD - m.costAtStart
			}
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if m.working {
			m.viewport.SetContent(m.renderFeed())
			m.viewport.GotoBottom()
		}
		return m, cmd

	case codeReplyMsg:
		m.working = false
		m.setAgentStatus("Coordinator", "ready")
		m.setAgentStatus("Code Agent", "ready")
		switch {
		case msg.err != nil:
			m.addEntry("system", "✗ "+msg.err.Error(), "error")
		case strings.HasPrefix(msg.content, tui.ConnectionErrorPrefix):
			// The client returns the offline notice as the response body; show it
			// as a red system message (and in the chat panel) rather than a
			// normal coordinator reply.
			m.addEntry("system", msg.content, "error")
			m.chat = append(m.chat, ChatLine{Role: "agent", Agent: "system", Content: msg.content})
			m.memOffline = true
		default:
			m.ingestReply(msg.content)
			// Also surface the coordinator's reply in the CHAT panel so it is
			// visible in team mode (the right panel renders the chat, not the
			// feed). Skip if streaming already populated it (see streamTokenMsg).
			if c := strings.TrimSpace(msg.content); c != "" && !m.streamedThisTurn {
				m.chat = append(m.chat, ChatLine{Role: "agent", Agent: "coordinator", Content: c})
			}
			m.streamedThisTurn = false
			m.progress.Percent = 100
		}
		m.viewport.SetContent(m.renderFeed())
		m.viewport.GotoBottom()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var icmd, vcmd tea.Cmd
	m.input, icmd = m.input.Update(msg)
	m.viewport, vcmd = m.viewport.Update(msg)
	return m, tea.Batch(icmd, vcmd)
}

// handleKey routes key presses: overlays first, then hotkeys, then the input.
func (m CodeModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Stop confirmation owns the keyboard while visible.
	if m.confirmStop {
		switch strings.ToLower(key) {
		case "y":
			m.confirmStop = false
			m.working = false
			m.paused = false
			m.addEntry("system", "■ Task stopped — work done so far is saved", "message")
			m.viewport.SetContent(m.renderFeed())
			m.viewport.GotoBottom()
		case "n", "esc":
			m.confirmStop = false
		}
		return m, nil
	}
	// Help overlay closes on ? or Esc.
	if m.helpOpen {
		if key == "?" || key == "esc" || key == "q" {
			m.helpOpen = false
		}
		return m, nil
	}

	switch key {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		// Toggle focus off the input so hotkeys (P/S/T/?/q) are reachable.
		if m.input.Focused() {
			m.input.Blur()
		} else {
			m.input.Focus()
		}
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		// Chatting with a specific agent → direct chat (does not start a task).
		if m.team && m.selectedAgent != "coordinator" {
			m.chat = append(m.chat, ChatLine{Role: "user", Content: text})
			m.input.Reset()
			return m, m.sendDirectChat(m.selectedAgent, text)
		}
		if m.working {
			return m, nil
		}
		m.addEntry("user", text, "message")
		// Mirror the user's message into the CHAT panel immediately so it is
		// visible in team mode (where the right panel shows the chat, not the
		// activity feed) before the coordinator replies.
		m.chat = append(m.chat, ChatLine{Role: "user", Content: text})
		m.input.Reset()
		m.working = true
		m.workStart = time.Now()
		m.costAtStart = 0
		if m.cost != nil {
			m.costAtStart = m.cost.TotalUSD
		}
		m.progress = TaskProgress{Step: "Planning..."}
		m.setAgentStatus("Coordinator", "busy")
		m.setAgentStatus("Code Agent", "busy")
		m.addEntry("coordinator", "Planning...", "message")
		m.viewport.SetContent(m.renderFeed())
		m.viewport.GotoBottom()
		return m, tea.Batch(m.submit(text), m.spin.Tick)
	}

	// Hotkeys work while the input is NOT focused (press Esc first).
	if !m.input.Focused() {
		switch key {
		case "p", "P":
			m.paused = !m.paused
			state := "⏸ PAUSED — agent activity frozen (press P to resume)"
			if !m.paused {
				state = "▶ Resumed"
			}
			m.addEntry("system", state, "message")
			m.viewport.SetContent(m.renderFeed())
			m.viewport.GotoBottom()
			return m, nil
		case "s", "S":
			if m.working {
				m.confirmStop = true
			}
			return m, nil
		case "t", "T":
			return m, m.forwardToTelegram()
		case "?":
			m.helpOpen = true
			return m, nil
		case "q":
			if m.standalone {
				return m, tea.Quit
			}
		}
	}

	var icmd, vcmd tea.Cmd
	m.input, icmd = m.input.Update(msg)
	m.viewport, vcmd = m.viewport.Update(msg)
	return m, tea.Batch(icmd, vcmd)
}

// forwardToTelegram sends the current task status through the messaging
// router (POST /api/notify).
func (m CodeModel) forwardToTelegram() tea.Cmd {
	c := m.client
	summary := m.statusSummary()
	return func() tea.Msg {
		if c == nil {
			return codeReplyMsg{err: fmt.Errorf("not connected")}
		}
		if err := c.Notify("📊 Task update from VORTEX", summary); err != nil {
			return codeReplyMsg{err: fmt.Errorf("telegram forward failed: %w", err)}
		}
		return codeReplyMsg{content: "→ Status forwarded to Telegram"}
	}
}

// statusSummary builds the human status text for the Telegram forward.
func (m CodeModel) statusSummary() string {
	var b strings.Builder
	if m.progress.Total > 0 {
		fmt.Fprintf(&b, "Step %d/%d", m.progress.Current, m.progress.Total)
		if m.progress.Step != "" {
			b.WriteString(" — " + m.progress.Step)
		}
		b.WriteString("\n")
	}
	// Include the last few feed lines for context.
	n := len(m.activity)
	for i := maxInt2(n-5, 0); i < n; i++ {
		b.WriteString(m.activity[i].Content + "\n")
	}
	if m.working {
		b.WriteString("⠸ Still working...")
	} else {
		b.WriteString("✓ Idle")
	}
	return b.String()
}

// setAgentStatus updates one roster entry.
func (m *CodeModel) setAgentStatus(name, status string) {
	for i := range m.agents {
		if m.agents[i].Name == name {
			m.agents[i].Status = status
		}
	}
}

// agentIcon maps a status to its brand icon.
func agentIcon(status string) string {
	switch status {
	case "busy":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorWarning)).Render(brand.IconBusy)
	case "ready":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorSuccess)).Render(brand.IconPulse)
	case "error":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorDanger)).Render(brand.IconError)
	default: // idle
		return brand.StyleSubtitle.Render(brand.IconIdle)
	}
}

// View renders the full coding interface.
func (m CodeModel) View() string {
	if m.helpOpen {
		return m.renderHelp()
	}
	header := m.renderHeader()
	var body string
	if m.team {
		// Three-panel collaboration layout: roster | comms | chat/checkpoint.
		body = m.renderThreePanel()
	} else {
		left := m.renderSidebar()
		feed := m.viewport.View()
		if feed == "" {
			feed = m.renderFeed()
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, " "+brand.IconVSep+" ", feed)
	}

	// Bottom bar: who you're chatting with + the input.
	chatting := brand.StyleSubtitle.Render("Chatting with: ") +
		lipgloss.NewStyle().Foreground(lipgloss.Color(agentColor(m.selectedAgent))).Render(m.selectAgentName())
	inputLine := m.input.View()
	if m.working {
		inputLine = m.spin.View() + " Working...  " + inputLine
	}
	footer := brand.StyleHelp.Render("[1-4] Switch agent  [C] Checkpoint  [P] Pause  [S] Stop  [T] Telegram  [?] Help" + m.quitHint())

	var overlay string
	if m.confirmStop {
		overlay = m.renderStopConfirm()
	}
	parts := []string{header, body, chatting, inputLine, footer}
	if overlay != "" {
		parts = []string{header, overlay, chatting, inputLine, footer}
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// quitHint shows [q] Quit only where q actually quits (standalone mode).
func (m CodeModel) quitHint() string {
	if m.standalone {
		return "  [q] Quit"
	}
	return ""
}

// renderHeader renders the top bar: VORTEX CODE, team/solo, project, cost.
func (m CodeModel) renderHeader() string {
	parts := []string{brand.StyleTitle.Render("▲ VORTEX CODE")}
	if m.team {
		parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorPrimary)).Render("👥 Team"))
	} else {
		parts = append(parts, brand.StyleSubtitle.Render("👤 Solo"))
	}
	if m.project != "" {
		parts = append(parts, brand.StyleSubtitle.Render(m.project))
	}
	provider := m.model
	if provider == "" && m.cost != nil {
		provider = m.cost.Provider
	}
	if provider != "" {
		parts = append(parts, tui.Pill(provider, brand.ColorPurple))
	}
	if m.cost != nil {
		parts = append(parts, tui.Pill(brand.IconCost+" "+brand.FormatCost(m.cost.TotalUSD), brand.ColorSuccess))
	}
	if m.paused {
		parts = append(parts, brand.StyleWarn.Render("⏸ PAUSED"))
	}
	return strings.Join(parts, "  ")
}

// renderSidebar renders the agents / memory / progress / skills panel.
func (m CodeModel) renderSidebar() string {
	var b strings.Builder
	sec := func(title string) {
		b.WriteString(brand.StyleTitle.Render(title) + "\n")
	}
	div := func() {
		b.WriteString(brand.StyleSubtitle.Render(strings.Repeat(brand.IconSep, codeSidebarWidth-2)) + "\n")
	}

	sec("AGENTS")
	// Team mode shows the 4-agent roster; solo mode shows a single agent.
	roster := m.agents
	if !m.team {
		roster = []CodeAgentStatus{{Name: "Agent", Role: "all", Status: m.agents[0].Status}}
	}
	for _, a := range roster {
		b.WriteString(fmt.Sprintf("%s %-12s %s\n", agentIcon(a.Status), a.Name, brand.StyleSubtitle.Render(a.Status)))
	}
	div()

	if m.projectInfo != nil {
		sec("PROJECT")
		b.WriteString(brand.IconFolder + " " + truncateStr(m.projectInfo.Name, codeSidebarWidth-4) + "\n")
		if len(m.projectInfo.Stack) > 0 {
			b.WriteString(brand.StyleSubtitle.Render(truncateStr(strings.Join(m.projectInfo.Stack, " · "), codeSidebarWidth-2)) + "\n")
		}
		if m.projectInfo.TestCmd != "" {
			b.WriteString(brand.StyleSubtitle.Render("Tests: "+truncateStr(m.projectInfo.TestCmd, codeSidebarWidth-9)) + "\n")
		}
		div()
	}

	sec("MEMORY")
	switch {
	case m.stats != nil:
		fmt.Fprintf(&b, "Skills:   %3d learned\n", m.stats.Skills)
		fmt.Fprintf(&b, "Episodes: %3d stored\n", m.stats.Episodes)
		fmt.Fprintf(&b, "Sessions: %3d total\n", m.stats.Sessions)
	case m.memOffline:
		b.WriteString(brand.StyleError.Render("✗ Server offline") + "\n")
	default:
		b.WriteString(brand.StyleSubtitle.Render("(connecting...)") + "\n")
	}
	div()

	sec("TASK PROGRESS")
	if m.working || m.progress.Percent > 0 {
		b.WriteString(brand.ProgressBar(m.progress.Percent, codeSidebarWidth-8) +
			fmt.Sprintf(" %3.0f%%\n", m.progress.Percent))
		if m.progress.Total > 0 {
			fmt.Fprintf(&b, "Step %d of %d\n", m.progress.Current, m.progress.Total)
		}
		if m.working {
			fmt.Fprintf(&b, "Elapsed: %s\n", brand.FormatDuration(time.Since(m.workStart)))
		}
		fmt.Fprintf(&b, "Cost so far: %s\n", brand.FormatCost(m.progress.CostUSD))
	} else {
		b.WriteString(brand.StyleSubtitle.Render("(no task running)") + "\n")
	}

	if len(m.skills) > 0 {
		div()
		sec("SKILLS USED")
		for _, s := range m.skills {
			b.WriteString(brand.IconArrow + " " + truncateStr(s, codeSidebarWidth-4) + "\n")
		}
	}
	return lipgloss.NewStyle().Width(codeSidebarWidth).Render(b.String())
}

// renderFeed renders the timestamped activity feed with step framing.
func (m CodeModel) renderFeed() string {
	w := maxInt2(m.viewport.Width, 40)
	var b strings.Builder
	for _, e := range m.activity {
		ts := brand.StyleSubtitle.Render("[" + e.Timestamp.Format("15:04:05") + "]")
		switch {
		case e.Source == "user":
			b.WriteString(ts + " " + lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorPrimary)).
				Render(brand.IconUser+" You: "+e.Content) + "\n")
		case e.Kind == "step-start":
			b.WriteString(brand.StyleSubtitle.Render("┌─ "+e.Content+" "+
				strings.Repeat("─", maxInt2(w-lipgloss.Width(e.Content)-6, 1))) + "\n")
		case e.Kind == "step-item":
			b.WriteString(brand.StyleSubtitle.Render("│ ") + brand.IconArrow + " " +
				strings.TrimLeft(e.Content, "›•- ") + "\n")
		case e.Kind == "step-end":
			b.WriteString(brand.StyleSubtitle.Render("└─ ") + brand.StyleSuccess.Render(e.Content) + "\n")
		case e.Kind == "error":
			b.WriteString(ts + " " + brand.StyleError.Render(e.Content) + "\n")
		case e.Source == "system":
			b.WriteString(ts + " " + brand.StyleSubtitle.Render(brand.IconInfo+" "+e.Content) + "\n")
		default:
			b.WriteString(ts + " " + lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorPurple)).
				Render(brand.IconAgent+" "+e.Source+": ") + e.Content + "\n")
		}
	}
	if m.working {
		b.WriteString(brand.StyleSubtitle.Render("│ ") + m.spin.View() + " Working...\n")
	}
	return b.String()
}

// renderStopConfirm renders the stop confirmation box.
func (m CodeModel) renderStopConfirm() string {
	box := brand.StyleApproval.Padding(1, 2).Render(
		brand.StyleWarn.Render("Stop task?") + "\n\n" +
			"This will stop all agents immediately.\n" +
			"Work done so far will be saved.\n\n" +
			"[Y] Stop    [N] Continue")
	return lipgloss.Place(maxInt2(m.width, 40), maxInt2(m.height-4, 8),
		lipgloss.Center, lipgloss.Center, box)
}

// renderHelp renders the ?-overlay.
func (m CodeModel) renderHelp() string {
	var b strings.Builder
	b.WriteString(brand.StyleTitle.Render("Help — VORTEX Code") + "\n\n")
	b.WriteString(brand.StyleTitle.Render("Keyboard shortcuts") + "\n")
	for _, it := range [][2]string{
		{"Enter", "Send task / instruction"},
		{"Esc", "Toggle input focus (hotkeys work unfocused)"},
		{"P", "Pause/resume agents"},
		{"S", "Stop task (with confirmation)"},
		{"T", "Forward status to Telegram"},
		{"?", "This help"},
		{"Ctrl+C", "Abort and quit"},
	} {
		fmt.Fprintf(&b, "  %-8s %s\n", it[0], it[1])
	}
	b.WriteString("\n" + brand.StyleTitle.Render("How it works") + "\n")
	for i, s := range []string{
		"Type your coding task", "Coordinator plans the work",
		"Specialist agents execute", "You see every step live",
		"Approve the final result",
	} {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, s)
	}
	b.WriteString("\n" + brand.StyleTitle.Render("Example tasks") + "\n")
	for _, s := range []string{
		`"build a REST API with JWT auth"`, `"add tests to all Python files"`,
		`"refactor main.py to use async"`, `"fix the bug on line 45 of auth.py"`,
	} {
		b.WriteString("  " + s + "\n")
	}
	b.WriteString("\n" + brand.StyleSubtitle.Render("Press ? or Esc to close"))
	box := brand.StyleActive.Padding(1, 2).Render(b.String())
	return lipgloss.Place(maxInt2(m.width, 50), maxInt2(m.height-1, 20),
		lipgloss.Center, lipgloss.Center, box)
}

// IsInputFocused reports whether the chat input owns the keyboard (the app
// shell disables navigation keys while true).
func (m CodeModel) IsInputFocused() bool { return m.input.Focused() }

// Accessors for tests.
func (m CodeModel) Paused() bool                   { return m.paused }
func (m CodeModel) Working() bool                  { return m.working }
func (m CodeModel) ConfirmingStop() bool           { return m.confirmStop }
func (m CodeModel) HelpOpen() bool                 { return m.helpOpen }
func (m CodeModel) Activity() []ActivityEntry      { return m.activity }
func (m CodeModel) AgentRoster() []CodeAgentStatus { return m.agents }
func (m CodeModel) Progress() TaskProgress         { return m.progress }
func (m CodeModel) TeamMode() bool                 { return m.team }
func (m CodeModel) ProjectInfo() *ProjectInfo      { return m.projectInfo }

// maxInt2 returns the larger of a and b.
func maxInt2(a, b int) int {
	if a > b {
		return a
	}
	return b
}
