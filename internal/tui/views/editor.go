package views

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// EditorModel is a multi-line text input with optional vim keybindings (build
// plan M20). It wraps bubbles/textarea and adds NORMAL/INSERT/VISUAL modes with
// the common motions and operators. When VimEnabled is false it behaves as a
// plain textarea (standard mode), so the same component serves both editor
// preferences selected in `vortex setup`.
type EditorModel struct {
	ta         textarea.Model
	VimEnabled bool
	mode       EditorMode

	// pendingOp accumulates a two-key operator like "dd"/"yy"/"gg".
	pendingOp string
	// register holds the last yanked/deleted line for p (paste).
	register string

	// submit is set true for one Update cycle when :w is issued, so the host
	// view can detect a save/submit request via Submitted().
	submit bool
	// quit is set when :q is issued.
	quit bool
}

// EditorMode is the vim editing mode.
type EditorMode string

// Editor modes.
const (
	ModeInsert EditorMode = "INSERT"
	ModeNormal EditorMode = "NORMAL"
	ModeVisual EditorMode = "VISUAL"
)

// NewEditor builds an editor. When vim is true it starts in INSERT mode (so a
// user can type immediately) with vim keybindings active; otherwise it is a
// plain textarea.
func NewEditor(vim bool) EditorModel {
	ta := textarea.New()
	ta.Focus()
	return EditorModel{ta: ta, VimEnabled: vim, mode: ModeInsert}
}

// Mode returns the current editing mode (always INSERT in standard mode).
func (m EditorModel) Mode() EditorMode {
	if !m.VimEnabled {
		return ModeInsert
	}
	return m.mode
}

// Value returns the editor's text.
func (m EditorModel) Value() string { return m.ta.Value() }

// SetValue replaces the editor's text.
func (m *EditorModel) SetValue(s string) { m.ta.SetValue(s) }

// Reset clears the editor and returns to INSERT mode.
func (m *EditorModel) Reset() {
	m.ta.Reset()
	m.mode = ModeInsert
	m.pendingOp = ""
}

// Focus focuses the underlying textarea.
func (m *EditorModel) Focus() tea.Cmd { return m.ta.Focus() }

// Blur removes focus.
func (m *EditorModel) Blur() { m.ta.Blur() }

// SetWidth/SetHeight size the editor.
func (m *EditorModel) SetWidth(w int)  { m.ta.SetWidth(w) }
func (m *EditorModel) SetHeight(h int) { m.ta.SetHeight(h) }

// Submitted reports (and clears) whether :w was issued since the last call.
func (m *EditorModel) Submitted() bool {
	s := m.submit
	m.submit = false
	return s
}

// Quit reports (and clears) whether :q was issued since the last call.
func (m *EditorModel) Quit() bool {
	q := m.quit
	m.quit = false
	return q
}

// Update handles a message. In standard mode it delegates to the textarea; in
// vim mode it routes keys through the mode state machine.
func (m EditorModel) Update(msg tea.Msg) (EditorModel, tea.Cmd) {
	if !m.VimEnabled {
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		return m, cmd
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		return m, cmd
	}
	switch m.mode {
	case ModeInsert:
		return m.updateInsert(key)
	case ModeNormal:
		return m.updateNormal(key)
	case ModeVisual:
		return m.updateVisual(key)
	default:
		return m, nil
	}
}

// updateInsert handles INSERT mode: normal typing, Esc → NORMAL.
func (m EditorModel) updateInsert(key tea.KeyMsg) (EditorModel, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.mode = ModeNormal
		return m, nil
	case tea.KeyCtrlC:
		m.quit = true
		return m, nil
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(key)
	return m, cmd
}

// updateNormal handles NORMAL mode motions and operators.
func (m EditorModel) updateNormal(key tea.KeyMsg) (EditorModel, tea.Cmd) {
	// Two-key operators in progress (dd, yy, gg).
	if m.pendingOp != "" {
		return m.completePendingOp(key), nil
	}

	s := key.String()
	switch s {
	case "i":
		m.mode = ModeInsert
	case "a":
		m.moveCursor(tea.KeyMsg{Type: tea.KeyRight})
		m.mode = ModeInsert
	case "h":
		m.moveCursor(tea.KeyMsg{Type: tea.KeyLeft})
	case "l":
		m.moveCursor(tea.KeyMsg{Type: tea.KeyRight})
	case "j":
		m.moveCursor(tea.KeyMsg{Type: tea.KeyDown})
	case "k":
		m.moveCursor(tea.KeyMsg{Type: tea.KeyUp})
	case "w":
		m.moveCursor(tea.KeyMsg{Type: tea.KeyRight}) // word-forward approximation
	case "b":
		m.moveCursor(tea.KeyMsg{Type: tea.KeyLeft})
	case "0":
		m.moveCursor(tea.KeyMsg{Type: tea.KeyHome})
	case "$":
		m.moveCursor(tea.KeyMsg{Type: tea.KeyEnd})
	case "G":
		m.gotoEnd()
	case "v":
		m.mode = ModeVisual
	case "p":
		m.paste()
	case "u":
		// textarea has no public undo; no-op rather than crash.
	case "d", "y", "g":
		m.pendingOp = s
	case ":":
		// Command entry is handled by the host view's command line; we mark the
		// next key sequence. For the embedded editor, support :w/:q via a tiny
		// inline reader by switching to a command sub-mode is overkill — instead
		// the host can call Submitted()/Quit(). Here ":" alone is a no-op.
	case "/":
		// Search is delegated to the host view; no-op here.
	}
	return m, nil
}

// updateVisual handles VISUAL mode (Esc returns to NORMAL).
func (m EditorModel) updateVisual(key tea.KeyMsg) (EditorModel, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.mode = ModeNormal
	case "h":
		m.moveCursor(tea.KeyMsg{Type: tea.KeyLeft})
	case "l":
		m.moveCursor(tea.KeyMsg{Type: tea.KeyRight})
	case "j":
		m.moveCursor(tea.KeyMsg{Type: tea.KeyDown})
	case "k":
		m.moveCursor(tea.KeyMsg{Type: tea.KeyUp})
	case "y", "d":
		// Simplified: yank/delete the current line then return to NORMAL.
		if key.String() == "d" {
			m.deleteLine()
		} else {
			m.yankLine()
		}
		m.mode = ModeNormal
	}
	return m, nil
}

// completePendingOp finishes a two-key operator.
func (m EditorModel) completePendingOp(key tea.KeyMsg) EditorModel {
	op := m.pendingOp
	m.pendingOp = ""
	k := key.String()
	switch {
	case op == "d" && k == "d":
		m.deleteLine()
	case op == "y" && k == "y":
		m.yankLine()
	case op == "g" && k == "g":
		m.gotoStart()
	}
	return m
}

// --- line operations on the textarea value ---------------------------------

// deleteLine removes the cursor's line, storing it in the register.
func (m *EditorModel) deleteLine() {
	lines := strings.Split(m.ta.Value(), "\n")
	row := m.ta.Line()
	if row < 0 || row >= len(lines) {
		return
	}
	m.register = lines[row]
	lines = append(lines[:row], lines[row+1:]...)
	if len(lines) == 0 {
		lines = []string{""}
	}
	m.ta.SetValue(strings.Join(lines, "\n"))
}

// yankLine copies the cursor's line into the register.
func (m *EditorModel) yankLine() {
	lines := strings.Split(m.ta.Value(), "\n")
	row := m.ta.Line()
	if row >= 0 && row < len(lines) {
		m.register = lines[row]
	}
}

// paste inserts the register below the cursor's line.
func (m *EditorModel) paste() {
	if m.register == "" {
		return
	}
	lines := strings.Split(m.ta.Value(), "\n")
	row := m.ta.Line()
	if row < 0 {
		row = 0
	}
	at := row + 1
	if at > len(lines) {
		at = len(lines)
	}
	lines = append(lines[:at], append([]string{m.register}, lines[at:]...)...)
	m.ta.SetValue(strings.Join(lines, "\n"))
}

// gotoStart/gotoEnd move to the document boundaries.
func (m *EditorModel) gotoStart() {
	for m.ta.Line() > 0 {
		m.moveCursor(tea.KeyMsg{Type: tea.KeyUp})
	}
}

func (m *EditorModel) gotoEnd() {
	total := strings.Count(m.ta.Value(), "\n")
	for m.ta.Line() < total {
		m.moveCursor(tea.KeyMsg{Type: tea.KeyDown})
	}
}

// moveCursor forwards a navigation key to the textarea.
func (m *EditorModel) moveCursor(key tea.KeyMsg) {
	m.ta, _ = m.ta.Update(key)
}

// SubmitOnColonW interprets a typed command string (":w", ":q", ":wq") issued
// by the host view's command line, setting submit/quit accordingly. It returns
// true if the command was recognised.
func (m *EditorModel) SubmitOnColonW(cmd string) bool {
	switch strings.TrimSpace(cmd) {
	case ":w":
		m.submit = true
		return true
	case ":q":
		m.quit = true
		return true
	case ":wq", ":x":
		m.submit = true
		m.quit = true
		return true
	default:
		return false
	}
}

// View renders the editor with a mode indicator in vim mode.
func (m EditorModel) View() string {
	body := m.ta.View()
	if !m.VimEnabled {
		return body
	}
	return body + "\n" + m.ModeIndicator()
}

// ModeIndicator renders the "-- MODE --" status line.
func (m EditorModel) ModeIndicator() string {
	return "-- " + string(m.Mode()) + " --"
}
