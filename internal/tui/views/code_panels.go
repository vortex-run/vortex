package views

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vortex-run/vortex/internal/tui"
	"github.com/vortex-run/vortex/internal/tui/brand"
)

// commsPanelWidth is the fixed middle (AGENT COMMS) panel width.
const commsPanelWidth = 38

// agentColor returns the brand color for an agent's messages.
func agentColor(agent string) string {
	switch agent {
	case "coordinator":
		return brand.ColorPrimary
	case "code-agent":
		return brand.ColorSuccess
	case "test-agent":
		return brand.ColorWarning
	case "review-agent":
		return brand.ColorPurple
	case "user":
		return brand.ColorText
	default:
		return brand.ColorTextDim
	}
}

// rosterNameByID maps a bus agent ID to its LEFT-roster display name.
var rosterNameByID = map[string]string{
	"coordinator":  "Coordinator",
	"code-agent":   "Code Agent",
	"test-agent":   "Test Agent",
	"review-agent": "Review",
}

// applyCommsStatus updates the roster from a live bus entry: a task assignment
// marks the recipient busy; a result marks the sender ready. Unknown IDs and
// the "user" pseudo-agent are ignored.
func (m *CodeModel) applyCommsStatus(e CommsEntry) {
	switch e.Kind {
	case "task":
		if name, ok := rosterNameByID[e.To]; ok && e.To != "user" {
			m.setAgentStatus(name, "busy")
		}
	case "result":
		if name, ok := rosterNameByID[e.From]; ok && e.From != "user" {
			m.setAgentStatus(name, "ready")
		}
	}
}

// streamCommsToChat surfaces live bus traffic in the CHAT panel while a
// coordinator task runs, so the conversation appears progressively. Only
// substantive lines (task hand-offs, results) are shown; raw progress noise
// stays in the comms panel. Sets streamedThisTurn so the final reply does not
// duplicate the content.
func (m *CodeModel) streamCommsToChat(e CommsEntry) {
	if !m.working || m.selectedAgent != "coordinator" {
		return
	}
	var line string
	switch e.Kind {
	case "task":
		if name, ok := rosterNameByID[e.To]; ok && e.To != "user" {
			line = "→ " + name + ": " + truncateStr(e.Content, 200)
		}
	case "result":
		if name, ok := rosterNameByID[e.From]; ok && e.From != "user" {
			line = "✓ " + name + ": " + truncateStr(e.Content, 200)
		}
	}
	if line == "" {
		return
	}
	// Append as a step line. The final codeReplyMsg still adds the coordinator's
	// summary, so these read as the work log leading up to the conclusion.
	m.chat = append(m.chat, ChatLine{Role: "agent", Agent: "coordinator", Content: line})
}

// HandleAGUI processes the three-panel collaboration messages and keys (bus
// comms, checkpoints, direct-chat replies, agent selection, checkpoint review).
// It returns (handled) so the main Update can fall through when it does not
// apply. The model is updated in place via the pointer receiver semantics of
// the bubbletea value model — callers reassign the returned model.
func (m CodeModel) HandleAGUI(msg tea.Msg) (CodeModel, bool) {
	switch msg := msg.(type) {
	case CommsMsg:
		e := CommsEntry(msg)
		// tool_result messages become collapsible tool-use rows, not feed lines.
		if e.Kind == "tool_result" {
			if tr := parseToolResult(e.Content); tr != nil {
				m.toolResults = append(m.toolResults, *tr)
				m.toolCursor = len(m.toolResults) - 1 // focus the newest row
			}
			return m, true
		}
		// The plan is shown prominently in the chat panel before execution.
		if e.Kind == "plan" {
			m.chat = append(m.chat, ChatLine{Role: "agent", Agent: "coordinator", Content: e.Content})
			return m, true
		}
		m.comms = append(m.comms, e)
		if len(m.comms) > 500 {
			m.comms = m.comms[len(m.comms)-500:]
		}
		// Drive the LEFT roster from live bus traffic: a task hand-off marks the
		// recipient busy; a result marks the sender ready again.
		m.applyCommsStatus(e)
		// Stream the live conversation into the CHAT panel while a coordinator
		// task is running, so the response builds up step-by-step (Claude-Code
		// feel) instead of appearing all at once at the end.
		m.streamCommsToChat(e)
		return m, true
	case CheckpointMsg:
		cp := CheckpointUI(msg)
		m.checkpoint = &cp
		m.viewingFile = -1
		m.input.Blur()                                           // so the V/E/A/R/S review keys are immediately active
		m.checkpointFlashUntil = time.Now().Add(2 * time.Second) // amber top-bar flash
		m.comms = append(m.comms, CommsEntry{
			Time: time.Now(), From: cp.FromAgent, To: "user", Kind: "checkpoint",
			Content: cp.Description,
		})
		// Ring the terminal bell so an away-from-keyboard user is alerted.
		m.pendingCmd = ringBell
		return m, true
	case DirectReplyMsg:
		m.chat = append(m.chat, ChatLine{Role: "agent", Agent: msg.Agent, Content: msg.Content})
		return m, true
	case tea.KeyMsg:
		return m.handleAGUIKey(msg)
	}
	return m, false
}

// handleAGUIKey handles 1-4 (agent select), C (checkpoint), and checkpoint
// review keys (V/E/A/R/S). Returns handled=false when the key is not ours.
func (m CodeModel) handleAGUIKey(msg tea.KeyMsg) (CodeModel, bool) {
	key := msg.String()

	// Viewing a file: Esc closes the viewer.
	if m.viewingFile >= 0 {
		if key == "esc" {
			m.viewingFile = -1
			return m, true
		}
		return m, true // swallow other keys while viewing
	}

	// Inline checkpoint-file editor owns the keyboard while open.
	if m.editor != nil {
		return m.handleEditorKey(msg)
	}

	// Checkpoint review mode.
	if m.checkpoint != nil && !m.input.Focused() {
		switch strings.ToLower(key) {
		case "v":
			if len(m.checkpoint.Files) > 0 {
				m.viewingFile = 0
			}
			return m, true
		case "e":
			// Open the inline editor (file picker first when multiple files).
			m.openCheckpointEditor()
			return m, true
		case "a":
			id := m.checkpoint.ID
			m.recordCheckpoint("approved", "✓ APPROVED by user")
			m.pendingCmd = m.checkpointActionCmd(id, "approve", nil)
			return m, true
		case "r":
			id := m.checkpoint.ID
			m.recordCheckpoint("rejected", "✗ REJECTED by user")
			m.pendingCmd = m.checkpointActionCmd(id, "reject", nil)
			return m, true
		case "s":
			m.recordCheckpoint("skipped", "⏭ Checkpoint skipped")
			return m, true
		}
	}

	// Agent selection 1-4 (only when input not focused, so digits can still be
	// typed into a message).
	if !m.input.Focused() {
		switch key {
		case "1", "2", "3", "4":
			idx := int(key[0] - '1')
			m.selectedAgent = agentIDByIndex[idx]
			m.input.Placeholder = "Ask " + m.selectAgentName() + " anything or give instructions..."
			return m, true
		case "c", "C":
			// C focuses the pending checkpoint, if any (already shown when set).
			if m.checkpoint != nil {
				return m, true
			}
		}
	}
	return m, false
}

// sendDirectChat returns a command that POSTs a direct-chat message to an agent
// and delivers the reply as a DirectReplyMsg.
func (m CodeModel) sendDirectChat(agentID, text string) tea.Cmd {
	c := m.client
	sid := m.sessionID
	return func() tea.Msg {
		if c == nil {
			return DirectReplyMsg{Agent: agentID, Content: "(not connected)"}
		}
		reply, err := c.AgentChat(agentID, sid, text)
		if err != nil {
			return DirectReplyMsg{Agent: agentID, Content: "⚠ " + err.Error()}
		}
		return DirectReplyMsg{Agent: agentID, Content: reply}
	}
}

// recordCheckpoint clears the active checkpoint and logs the decision.
func (m *CodeModel) recordCheckpoint(status, label string) {
	if m.checkpoint == nil {
		return
	}
	m.comms = append(m.comms, CommsEntry{
		Time: time.Now(), From: "user", To: m.checkpoint.FromAgent, Kind: status, Content: label,
	})
	m.checkpoint = nil
	m.viewingFile = -1
}

// ringBell emits the terminal bell (\a) so the user is alerted to a checkpoint
// even when away from the screen.
func ringBell() tea.Msg {
	fmt.Print("\a")
	return nil
}

// renderThreePanel composes the LEFT (roster) | MIDDLE (comms) | RIGHT (chat
// or checkpoint) layout.
func (m CodeModel) renderThreePanel() string {
	left := m.renderSidebar()
	middle := m.renderComms()
	var right string
	switch {
	case m.editor != nil:
		right = m.renderEditor()
	case m.viewingFile >= 0:
		right = m.renderFileViewer()
	case m.checkpoint != nil:
		right = m.renderCheckpoint()
	default:
		right = m.renderChat()
	}
	sep := " " + brand.StyleSubtitle.Render(brand.IconVSep) + " "
	return lipgloss.JoinHorizontal(lipgloss.Top, left, sep, middle, sep, right)
}

// renderComms renders the middle AGENT COMMS panel.
func (m CodeModel) renderComms() string {
	var b strings.Builder
	b.WriteString(brand.StyleTitle.Render("AGENT COMMS") + "\n")
	b.WriteString(brand.StyleSubtitle.Render(strings.Repeat(brand.IconSep, commsPanelWidth-2)) + "\n")
	start := 0
	if len(m.comms) > 30 {
		start = len(m.comms) - 30
	}
	for _, e := range m.comms[start:] {
		ts := brand.StyleSubtitle.Render(e.Time.Format("15:04"))
		switch e.Kind {
		case "checkpoint":
			b.WriteString(ts + " " + brand.StyleWarn.Render("⏸ CHECKPOINT") + "\n")
			b.WriteString("  " + brand.StyleSubtitle.Render(truncateStr(e.Content, commsPanelWidth-4)) + "\n")
		case "approved":
			b.WriteString(ts + " " + brand.StyleSuccess.Render("✓ APPROVED") + "\n")
		case "rejected":
			b.WriteString(ts + " " + brand.StyleError.Render("✗ REJECTED") + "\n")
		case "edited":
			b.WriteString(ts + " " + lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorPurple)).Render("✎ EDITED") + "\n")
		default:
			fromC := lipgloss.NewStyle().Foreground(lipgloss.Color(agentColor(e.From)))
			arrow := ""
			if e.To != "" {
				arrow = " → " + e.To
			}
			b.WriteString(ts + " " + fromC.Render(e.From) + brand.StyleSubtitle.Render(arrow) + "\n")
			b.WriteString("  " + truncateStr(e.Content, commsPanelWidth-4) + "\n")
		}
	}
	return lipgloss.NewStyle().Width(commsPanelWidth).Render(b.String())
}

// renderChat renders the right-panel conversation with the selected agent.
func (m CodeModel) renderChat() string {
	w := maxInt2(m.width-codeSidebarWidth-commsPanelWidth-8, 24)
	var b strings.Builder
	b.WriteString(brand.StyleTitle.Render("CHAT — "+m.selectAgentName()) + "\n")
	b.WriteString(brand.StyleSubtitle.Render(strings.Repeat(brand.IconSep, minInt(w, 30))) + "\n")
	if len(m.chat) == 0 {
		b.WriteString(brand.StyleSubtitle.Render("Talk to "+m.selectAgentName()+" — type below.") + "\n")
	}
	for _, line := range m.chat {
		switch {
		case line.Role == "user":
			b.WriteString(lipgloss.PlaceHorizontal(minInt(w, 40), lipgloss.Right,
				lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorPrimary)).Render("You: "+line.Content)) + "\n")
		case strings.HasPrefix(line.Content, tui.ConnectionErrorPrefix):
			// Connection error: render the whole notice in red as a system message.
			b.WriteString(brand.StyleError.Render(line.Content) + "\n")
		default:
			c := lipgloss.NewStyle().Foreground(lipgloss.Color(agentColor(m.selectedAgent)))
			b.WriteString(c.Render(line.Agent+": ") + line.Content + "\n")
		}
	}
	// Collapsible tool-use rows (write_file/run_terminal), newest last. The
	// focused row (toolCursor) is highlighted; Enter toggles it.
	for i := range m.toolResults {
		row := m.toolResults[i].render(w)
		if i == m.toolCursor {
			row = lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorPrimary)).Render("› ") + row
		} else {
			row = "  " + row
		}
		b.WriteString(row)
	}
	// Active option selector (arrow-key menu) takes over the input affordance.
	if m.selector != nil && m.selector.Active {
		b.WriteString("\n" + renderSelector(m.selector, w) + "\n")
	}
	// While a task is in flight: once tokens arrive the reply streams in live
	// with the spinner as its cursor (AGUI item C); before the first token,
	// the animated "thinking..." indicator (Claude-Code feel).
	if m.working {
		c := lipgloss.NewStyle().Foreground(lipgloss.Color(agentColor("coordinator")))
		if m.streamText != "" {
			b.WriteString(c.Render(m.selectAgentName()+": ") + m.streamText + " " + m.spin.View() + "\n")
		} else {
			b.WriteString(c.Render(brand.IconAgent+" "+m.selectAgentName()) + " " +
				m.spin.View() + brand.StyleSubtitle.Render(" thinking...") + "\n")
		}
	}
	return lipgloss.NewStyle().Width(w).Render(b.String())
}

// renderCheckpoint renders the right-panel checkpoint review as a bold,
// unmissable boxed overlay.
func (m CodeModel) renderCheckpoint() string {
	cp := m.checkpoint
	w := maxInt2(m.width-codeSidebarWidth-commsPanelWidth-10, 36)
	amber := lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorWarning)).Bold(true)

	var b strings.Builder
	b.WriteString(amber.Render("⏸  CHECKPOINT — Your review is needed") + "\n")
	b.WriteString(brand.StyleSubtitle.Render(strings.Repeat(brand.IconSep, minInt(w, 44))) + "\n\n")
	if cp.Description != "" {
		b.WriteString(cp.Description + "\n\n")
	}
	b.WriteString("Files produced:\n")
	for _, f := range cp.Files {
		tag := lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorSuccess)).Render("NEW")
		if !f.IsNew {
			tag = brand.StyleSubtitle.Render("MODIFIED")
		}
		fmt.Fprintf(&b, "%s %-24s %s %s\n", brand.IconFile, f.Path,
			brand.StyleSubtitle.Render(fmt.Sprintf("(%d lines)", f.Lines)), tag)
	}
	b.WriteString("\n")
	b.WriteString(brand.StyleHelp.Render("[V] View file") + "\n")
	b.WriteString(brand.StyleHelp.Render("[E] Edit file") + "\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(brand.ColorSuccess)).Render("[A] Approve") +
		brand.StyleSubtitle.Render(" — continue to "+agentDisplayName(cp.ToAgent)) + "\n")
	b.WriteString(brand.StyleError.Render("[R] Reject") +
		brand.StyleSubtitle.Render(" — stop the pipeline") + "\n")

	return brand.StyleApproval.Padding(1, 2).Width(maxInt2(w-4, 30)).Render(b.String())
}

// agentDisplayName maps an agent id to a friendly name for the checkpoint box.
func agentDisplayName(id string) string {
	if name, ok := rosterNameByID[id]; ok {
		return name
	}
	if id == "" {
		return "the next agent"
	}
	return id
}

// renderFileViewer renders a scrollable file content overlay (line-numbered).
func (m CodeModel) renderFileViewer() string {
	if m.checkpoint == nil || m.viewingFile < 0 || m.viewingFile >= len(m.checkpoint.Files) {
		return ""
	}
	f := m.checkpoint.Files[m.viewingFile]
	var b strings.Builder
	b.WriteString(brand.StyleTitle.Render(f.Path+fmt.Sprintf(" (%d lines)", f.Lines)) + "\n\n")
	for i, line := range strings.Split(f.Content, "\n") {
		fmt.Fprintf(&b, "%s %s\n", brand.StyleSubtitle.Render(fmt.Sprintf("%3d", i+1)), line)
		if i > 40 {
			b.WriteString(brand.StyleSubtitle.Render("  ...") + "\n")
			break
		}
	}
	b.WriteString("\n" + brand.StyleHelp.Render("[Esc] Close"))
	return brand.StyleBorder.Padding(0, 1).Render(b.String())
}

// --- accessors for tests ---------------------------------------------------

// SelectedAgent returns the chat target agent id (for tests).
func (m CodeModel) SelectedAgent() string { return m.selectedAgent }

// Comms returns the middle-panel entries (for tests).
func (m CodeModel) Comms() []CommsEntry { return m.comms }

// Chat returns the right-panel chat lines (for tests).
func (m CodeModel) Chat() []ChatLine { return m.chat }

// CheckpointActive reports whether a checkpoint is under review (for tests).
func (m CodeModel) CheckpointActive() bool { return m.checkpoint != nil }

// ViewingFile reports whether the file viewer is open (for tests).
func (m CodeModel) ViewingFile() bool { return m.viewingFile >= 0 }
