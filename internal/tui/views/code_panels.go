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
		m.comms = append(m.comms, CommsEntry{
			Time: time.Now(), From: cp.FromAgent, To: "user", Kind: "checkpoint",
			Content: cp.Description,
		})
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

	// Checkpoint review mode.
	if m.checkpoint != nil && !m.input.Focused() {
		switch strings.ToLower(key) {
		case "v":
			if len(m.checkpoint.Files) > 0 {
				m.viewingFile = 0
			}
			return m, true
		case "a":
			m.recordCheckpoint("approved", "✓ APPROVED by user")
			return m, true
		case "r":
			m.recordCheckpoint("rejected", "✗ REJECTED by user")
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

// renderThreePanel composes the LEFT (roster) | MIDDLE (comms) | RIGHT (chat
// or checkpoint) layout.
func (m CodeModel) renderThreePanel() string {
	left := m.renderSidebar()
	middle := m.renderComms()
	var right string
	if m.viewingFile >= 0 {
		right = m.renderFileViewer()
	} else if m.checkpoint != nil {
		right = m.renderCheckpoint()
	} else {
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
	// Active option selector (arrow-key menu) takes over the input affordance.
	if m.selector != nil && m.selector.Active {
		b.WriteString("\n" + renderSelector(m.selector, w) + "\n")
	}
	// Animated thinking indicator while a task is in flight (Claude-Code feel).
	if m.working {
		c := lipgloss.NewStyle().Foreground(lipgloss.Color(agentColor("coordinator")))
		b.WriteString(c.Render(brand.IconAgent+" "+m.selectAgentName()) + " " +
			m.spin.View() + brand.StyleSubtitle.Render(" thinking...") + "\n")
	}
	return lipgloss.NewStyle().Width(w).Render(b.String())
}

// renderCheckpoint renders the right-panel checkpoint review box.
func (m CodeModel) renderCheckpoint() string {
	cp := m.checkpoint
	var b strings.Builder
	b.WriteString(brand.StyleWarn.Render("⏸ CHECKPOINT — Review before continuing") + "\n\n")
	b.WriteString(cp.Description + "\n\n")
	b.WriteString("Files produced:\n")
	for _, f := range cp.Files {
		tag := "MODIFIED"
		if f.IsNew {
			tag = "NEW"
		}
		fmt.Fprintf(&b, "%s %s (%s — %d lines)\n", brand.IconFile, f.Path, tag, f.Lines)
	}
	b.WriteString("\n")
	b.WriteString(brand.StyleHelp.Render("[V] View file  [A] Approve  [R] Reject  [S] Skip"))
	box := brand.StyleApproval.Padding(1, 2).Render(b.String())
	return box
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
