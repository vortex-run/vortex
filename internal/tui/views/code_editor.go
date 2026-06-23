package views

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
	"github.com/vortex-run/vortex/internal/tui/brand"
)

// checkpointEditor is the inline editor for a checkpoint file. Ctrl+S saves
// (POSTs the edit and resolves the checkpoint as edited); Esc cancels.
type checkpointEditor struct {
	checkpointID string
	path         string
	ta           textarea.Model
}

// newCheckpointEditor builds an editor seeded with a file's content.
func newCheckpointEditor(checkpointID, path, content string, width, height int) *checkpointEditor {
	ta := textarea.New()
	ta.SetValue(content)
	ta.SetWidth(maxInt2(width, 30))
	ta.SetHeight(maxInt2(height, 8))
	ta.Focus()
	return &checkpointEditor{checkpointID: checkpointID, path: path, ta: ta}
}

// openCheckpointEditor opens the editor for the checkpoint's file. With multiple
// files it focuses the first (a future file picker can refine this); the file's
// current content is loaded from the checkpoint preview.
func (m *CodeModel) openCheckpointEditor() {
	if m.checkpoint == nil || len(m.checkpoint.Files) == 0 {
		return
	}
	f := m.checkpoint.Files[0]
	w := maxInt2(m.width-codeSidebarWidth-commsPanelWidth-10, 30)
	h := maxInt2(m.height-10, 8)
	m.editor = newCheckpointEditor(m.checkpoint.ID, f.Path, f.Content, w, h)
}

// handleEditorKey processes keys while the inline editor is open.
func (m CodeModel) handleEditorKey(msg tea.KeyMsg) (CodeModel, bool) {
	switch msg.String() {
	case "esc":
		m.editor = nil
		return m, true
	case "ctrl+s":
		ed := m.editor
		edits := []tui.CheckpointFileEdit{{Path: ed.path, Content: ed.ta.Value()}}
		m.pendingCmd = m.checkpointActionCmd(ed.checkpointID, "edit", edits)
		m.recordCheckpoint("edited", "✎ EDITED by user — "+ed.path)
		m.chat = append(m.chat, ChatLine{
			Role: "agent", Agent: "system",
			Content: "✓ Edit saved — continuing the pipeline.",
		})
		m.editor = nil
		return m, true
	default:
		var cmd tea.Cmd
		m.editor.ta, cmd = m.editor.ta.Update(msg)
		_ = cmd
		return m, true
	}
}

// checkpointActionCmd returns a command that POSTs a checkpoint decision to the
// server. action is "approve"/"reject"/"edit"; edits is used only for "edit".
func (m CodeModel) checkpointActionCmd(id, action string, edits []tui.CheckpointFileEdit) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		if c == nil {
			return DirectReplyMsg{Agent: "system", Content: "(not connected)"}
		}
		var err error
		switch action {
		case "approve":
			err = c.ApproveCheckpoint(id)
		case "reject":
			err = c.RejectCheckpoint(id, "rejected via vortex code")
		case "edit":
			err = c.EditCheckpoint(id, edits)
		}
		if err != nil {
			return DirectReplyMsg{Agent: "system", Content: "⚠ checkpoint " + action + ": " + err.Error()}
		}
		return checkpointResolvedMsg{action: action}
	}
}

// checkpointResolvedMsg signals a checkpoint server action completed.
type checkpointResolvedMsg struct{ action string }

// renderEditor renders the inline checkpoint-file editor.
func (m CodeModel) renderEditor() string {
	var b strings.Builder
	b.WriteString(brand.StyleTitle.Render("Editing: "+m.editor.path) + "\n")
	b.WriteString(m.editor.ta.View() + "\n")
	b.WriteString(brand.StyleHelp.Render("[Ctrl+S] Save and continue   [Esc] Cancel"))
	return brand.StyleBorder.Padding(0, 1).Render(b.String())
}
