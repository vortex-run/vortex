package views

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
)

func TestCode_ThreePanelLayout(t *testing.T) {
	m := sizedCode(WithTeam())
	out := m.View()
	for _, want := range []string{"AGENTS", "AGENT COMMS", "CHAT — Coordinator", "Chatting with:"} {
		if !strings.Contains(out, want) {
			t.Errorf("three-panel view missing %q:\n%s", want, out)
		}
	}
}

func TestCode_AgentSelection(t *testing.T) {
	m := blurred(sizedCode(WithTeam()))
	if m.SelectedAgent() != "coordinator" {
		t.Fatalf("default selection = %q, want coordinator", m.SelectedAgent())
	}
	cases := map[string]string{"2": "code-agent", "3": "test-agent", "4": "review-agent", "1": "coordinator"}
	for key, want := range cases {
		upd, handled := m.HandleAGUI(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if !handled {
			t.Errorf("key %q not handled", key)
		}
		m = upd
		if m.SelectedAgent() != want {
			t.Errorf("after %q, selection = %q, want %q", key, m.SelectedAgent(), want)
		}
	}
	// Bottom bar reflects the selected agent.
	m.selectedAgent = "code-agent"
	if !strings.Contains(m.View(), "Chatting with: ") || !strings.Contains(m.View(), "Code Agent") {
		t.Errorf("bottom bar should show Code Agent:\n%s", m.View())
	}
}

func TestCode_AgentSelectIgnoredWhileTyping(t *testing.T) {
	// Input focused (default) → digits go to the input, not agent selection.
	m := sizedCode(WithTeam())
	if !m.IsInputFocused() {
		t.Skip("input not focused by default")
	}
	_, handled := m.HandleAGUI(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if handled {
		t.Error("agent-select digit should not be consumed while input is focused")
	}
}

func TestCode_CommsPanelShowsMessages(t *testing.T) {
	m := sizedCode(WithTeam())
	m, _ = m.HandleAGUI(CommsMsg{Time: time.Now(), From: "coordinator", To: "code-agent", Content: "Write FastAPI auth"})
	m, _ = m.HandleAGUI(CommsMsg{Time: time.Now(), From: "code-agent", To: "coordinator", Content: "Reading files..."})
	if len(m.Comms()) != 2 {
		t.Fatalf("comms = %d, want 2", len(m.Comms()))
	}
	out := m.renderComms()
	for _, want := range []string{"AGENT COMMS", "coordinator", "Write FastAPI auth", "code-agent"} {
		if !strings.Contains(out, want) {
			t.Errorf("comms panel missing %q:\n%s", want, out)
		}
	}
}

func TestCode_CheckpointActivates(t *testing.T) {
	m := sizedCode(WithTeam())
	m, handled := m.HandleAGUI(CheckpointMsg{
		ID: "cp-1", Title: "Code Agent finished", Description: "Code Agent finished. Test Agent is next.",
		FromAgent: "code-agent", ToAgent: "test-agent",
		Files: []CheckpointFile{{Path: "main.py", Lines: 89, IsNew: true}},
	})
	if !handled || !m.CheckpointActive() {
		t.Fatal("checkpoint message should activate checkpoint review")
	}
	out := m.View()
	for _, want := range []string{"⏸ CHECKPOINT", "main.py", "[A] Approve", "[R] Reject"} {
		if !strings.Contains(out, want) {
			t.Errorf("checkpoint view missing %q:\n%s", want, out)
		}
	}
	// The checkpoint is logged in the comms panel.
	if !strings.Contains(m.renderComms(), "CHECKPOINT") {
		t.Error("checkpoint should appear in the comms feed")
	}
}

func TestCode_CheckpointApprove(t *testing.T) {
	m := blurred(sizedCodeWithCheckpoint(t))
	m, handled := m.HandleAGUI(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !handled {
		t.Fatal("A key not handled in checkpoint mode")
	}
	if m.CheckpointActive() {
		t.Error("A should clear the checkpoint")
	}
	if !strings.Contains(m.renderComms(), "APPROVED") {
		t.Error("approval should be logged in comms")
	}
}

func TestCode_CheckpointReject(t *testing.T) {
	m := blurred(sizedCodeWithCheckpoint(t))
	m, _ = m.HandleAGUI(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if m.CheckpointActive() {
		t.Error("R should clear the checkpoint")
	}
	if !strings.Contains(m.renderComms(), "REJECTED") {
		t.Error("rejection should be logged in comms")
	}
}

func TestCode_CheckpointViewFile(t *testing.T) {
	m := blurred(sizedCodeWithCheckpoint(t))
	m, handled := m.HandleAGUI(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if !handled || !m.ViewingFile() {
		t.Fatal("V should open the file viewer")
	}
	out := m.View()
	if !strings.Contains(out, "main.py") || !strings.Contains(out, "[Esc] Close") {
		t.Errorf("file viewer missing content:\n%s", out)
	}
	// Line numbers are shown.
	if !strings.Contains(out, "  1") {
		t.Error("file viewer should show line numbers")
	}
	// Esc closes it.
	m, _ = m.HandleAGUI(tea.KeyMsg{Type: tea.KeyEsc})
	if m.ViewingFile() {
		t.Error("Esc should close the file viewer")
	}
}

func TestCode_DirectReplyAddedToChat(t *testing.T) {
	m := sizedCode(WithTeam())
	m, handled := m.HandleAGUI(DirectReplyMsg{Agent: "code-agent", Content: "I used SQLite because..."})
	if !handled {
		t.Fatal("direct reply not handled")
	}
	if len(m.Chat()) != 1 || m.Chat()[0].Role != "agent" {
		t.Fatalf("chat = %+v", m.Chat())
	}
	if !strings.Contains(m.renderChat(), "SQLite") {
		t.Error("chat panel should show the agent reply")
	}
}

func TestCode_EnterToSelectedAgentChats(t *testing.T) {
	m := sizedCode(WithTeam())
	m.selectedAgent = "code-agent"
	m.input.SetValue("why sqlite?")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(CodeModel)
	// A user chat line was added (not a task submission).
	if len(m.Chat()) != 1 || m.Chat()[0].Content != "why sqlite?" {
		t.Errorf("chat = %+v", m.Chat())
	}
	if m.Working() {
		t.Error("chatting with an agent should not start a task")
	}
	if cmd == nil {
		t.Error("Enter should produce the direct-chat command")
	}
}

func TestCode_CoordinatorSubmitShowsUserMessageInChat(t *testing.T) {
	m := sizedCode(WithTeam())
	// selectedAgent defaults to "coordinator" → Enter starts a task.
	m.input.SetValue("create a python calculator")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(CodeModel)

	if !m.Working() {
		t.Error("coordinator submit should start a task (working)")
	}
	// The user message must appear immediately in the CHAT panel, not only the feed.
	if len(m.Chat()) != 1 || m.Chat()[0].Role != "user" || m.Chat()[0].Content != "create a python calculator" {
		t.Fatalf("chat = %+v, want one user line", m.Chat())
	}
	if !strings.Contains(m.renderChat(), "create a python calculator") {
		t.Errorf("CHAT panel should show the user message:\n%s", m.renderChat())
	}
}

func TestCode_CommsStreamReadyArmsListenLoop(t *testing.T) {
	m := sizedCode(WithTeam())
	ch := make(chan tui.CommsRecord, 1)
	updated, cmd := m.Update(commsStreamReadyMsg{ch: ch})
	m = updated.(CodeModel)
	if cmd == nil {
		t.Error("a ready stream should kick off the listen loop")
	}
	// A live comms message re-arms the loop (returns a follow-up cmd) and lands
	// in the middle panel.
	updated2, cmd2 := m.Update(CommsMsg{Time: time.Now(), From: "coordinator", To: "code-agent", Content: "go"})
	m = updated2.(CodeModel)
	if cmd2 == nil {
		t.Error("handling a CommsMsg must re-issue listenComms")
	}
	if len(m.Comms()) != 1 || m.Comms()[0].Content != "go" {
		t.Errorf("comms = %+v, want one entry", m.Comms())
	}
}

func TestCode_RosterStatusFromLiveComms(t *testing.T) {
	m := sizedCode(WithTeam())
	// A task hand-off to the test agent marks it busy.
	m, _ = m.HandleAGUI(CommsMsg{Time: time.Now(), From: "coordinator", To: "test-agent", Kind: "task", Content: "run tests"})
	if statusOf(m, "Test Agent") != "busy" {
		t.Errorf("Test Agent status = %q after task, want busy", statusOf(m, "Test Agent"))
	}
	// Its result marks it ready again.
	m, _ = m.HandleAGUI(CommsMsg{Time: time.Now(), From: "test-agent", To: "coordinator", Kind: "result", Content: "passed"})
	if statusOf(m, "Test Agent") != "ready" {
		t.Errorf("Test Agent status = %q after result, want ready", statusOf(m, "Test Agent"))
	}
}

// statusOf returns the roster status for the named agent (test helper).
func statusOf(m CodeModel, name string) string {
	for _, a := range m.AgentRoster() {
		if a.Name == name {
			return a.Status
		}
	}
	return ""
}

func TestCode_InputHistoryRecall(t *testing.T) {
	m := sizedCode(WithTeam())
	// Submit two coordinator tasks to build history.
	for _, msg := range []string{"first task", "second task"} {
		m.input.SetValue(msg)
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(CodeModel)
		// Clear working so the next Enter is accepted.
		updated2, _ := m.Update(codeReplyMsg{content: "ok"})
		m = updated2.(CodeModel)
	}
	if len(m.inputHistory) != 2 {
		t.Fatalf("history = %v, want 2 entries", m.inputHistory)
	}
	// ↑ recalls the most recent, ↑ again the older one.
	up := tea.KeyMsg{Type: tea.KeyUp}
	updated, _ := m.Update(up)
	m = updated.(CodeModel)
	if m.input.Value() != "second task" {
		t.Errorf("first ↑ = %q, want 'second task'", m.input.Value())
	}
	updated, _ = m.Update(up)
	m = updated.(CodeModel)
	if m.input.Value() != "first task" {
		t.Errorf("second ↑ = %q, want 'first task'", m.input.Value())
	}
	// ↓ moves back toward newest, then to an empty input.
	down := tea.KeyMsg{Type: tea.KeyDown}
	updated, _ = m.Update(down)
	m = updated.(CodeModel)
	if m.input.Value() != "second task" {
		t.Errorf("↓ = %q, want 'second task'", m.input.Value())
	}
	updated, _ = m.Update(down)
	m = updated.(CodeModel)
	if m.input.Value() != "" {
		t.Errorf("↓ past newest = %q, want empty", m.input.Value())
	}
}

func TestCode_LiveCommsStreamIntoChat(t *testing.T) {
	m := sizedCode(WithTeam())
	m.working = true // a coordinator task is in flight
	// A task hand-off and its result both surface as step lines in the chat.
	m, _ = m.HandleAGUI(CommsMsg{Time: time.Now(), From: "coordinator", To: "code-agent", Kind: "task", Content: "write the app"})
	m, _ = m.HandleAGUI(CommsMsg{Time: time.Now(), From: "code-agent", To: "coordinator", Kind: "result", Content: "created app.py"})

	out := m.renderChat()
	if !strings.Contains(out, "→ Code Agent") || !strings.Contains(out, "write the app") {
		t.Errorf("chat should stream the task hand-off:\n%s", out)
	}
	if !strings.Contains(out, "✓ Code Agent") || !strings.Contains(out, "created app.py") {
		t.Errorf("chat should stream the result:\n%s", out)
	}
}

func TestCode_NoCommsStreamWhenIdle(t *testing.T) {
	m := sizedCode(WithTeam()) // not working
	before := len(m.Chat())
	m, _ = m.HandleAGUI(CommsMsg{Time: time.Now(), From: "coordinator", To: "code-agent", Kind: "task", Content: "x"})
	if len(m.Chat()) != before {
		t.Error("comms should not stream into chat when no task is running")
	}
}

func TestCode_ThinkingIndicatorWhileWorking(t *testing.T) {
	m := sizedCode(WithTeam())
	m.working = true
	if !strings.Contains(m.renderChat(), "thinking...") {
		t.Errorf("chat should show a thinking indicator while working:\n%s", m.renderChat())
	}
	// Not shown once the task completes.
	updated, _ := m.Update(codeReplyMsg{content: "done"})
	m = updated.(CodeModel)
	if strings.Contains(m.renderChat(), "thinking...") {
		t.Error("thinking indicator should disappear after the task completes")
	}
}

func TestCode_CommsClosedResetsChannel(t *testing.T) {
	m := sizedCode(WithTeam())
	ch := make(chan tui.CommsRecord)
	m.commsCh = ch
	updated, _ := m.Update(commsClosedMsg{})
	m = updated.(CodeModel)
	if m.commsCh != nil {
		t.Error("a closed stream should clear commsCh so a tick reopens it")
	}
}

func TestCode_CoordinatorReplyShownInChat(t *testing.T) {
	m := sizedCode(WithTeam())
	m.working = true
	m.chat = append(m.chat, ChatLine{Role: "user", Content: "what can you do?"})
	updated, _ := m.Update(codeReplyMsg{content: "I coordinate a team of agents."})
	m = updated.(CodeModel)

	if m.Working() {
		t.Error("reply should clear working")
	}
	// The coordinator reply must be added as an agent line in the chat panel.
	last := m.Chat()[len(m.Chat())-1]
	if last.Role != "agent" || last.Agent != "coordinator" || last.Content != "I coordinate a team of agents." {
		t.Fatalf("last chat line = %+v, want coordinator agent reply", last)
	}
	if !strings.Contains(m.renderChat(), "I coordinate a team of agents.") {
		t.Errorf("CHAT panel should show the coordinator reply:\n%s", m.renderChat())
	}
}

func TestCode_CheckpointEditOpensEditor(t *testing.T) {
	m := blurred(sizedCodeWithCheckpoint(t))
	// E opens the inline editor for the checkpoint file.
	m, handled := m.HandleAGUI(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if !handled || m.editor == nil {
		t.Fatal("E should open the inline checkpoint editor")
	}
	if m.editor.path != "main.py" {
		t.Errorf("editor path = %q, want main.py", m.editor.path)
	}
	out := m.View()
	if !strings.Contains(out, "Editing: main.py") || !strings.Contains(out, "[Ctrl+S] Save") {
		t.Errorf("editor view missing:\n%s", out)
	}
}

func TestCode_CheckpointEditorEscCancels(t *testing.T) {
	m := blurred(sizedCodeWithCheckpoint(t))
	m, _ = m.HandleAGUI(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m, _ = m.HandleAGUI(tea.KeyMsg{Type: tea.KeyEsc})
	if m.editor != nil {
		t.Error("Esc should close the editor")
	}
	// The checkpoint is still pending (cancel didn't resolve it).
	if !m.CheckpointActive() {
		t.Error("cancelling the editor should keep the checkpoint pending")
	}
}

func TestCode_CheckpointEditorCtrlSProducesCommand(t *testing.T) {
	m := blurred(sizedCodeWithCheckpoint(t))
	m, _ = m.HandleAGUI(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m, _ = m.HandleAGUI(tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.editor != nil {
		t.Error("Ctrl+S should close the editor")
	}
	if m.pendingCmd == nil {
		t.Error("Ctrl+S should queue a checkpoint-edit command")
	}
	// The checkpoint review is resolved locally (edited).
	if m.CheckpointActive() {
		t.Error("Ctrl+S should resolve the checkpoint")
	}
}

// sizedCodeWithCheckpoint returns a team model with an active checkpoint.
func sizedCodeWithCheckpoint(t *testing.T) CodeModel {
	t.Helper()
	m := sizedCode(WithTeam())
	m, _ = m.HandleAGUI(CheckpointMsg{
		ID: "cp-1", Description: "Code Agent finished.",
		FromAgent: "code-agent", ToAgent: "test-agent",
		Files: []CheckpointFile{{Path: "main.py", Content: "from fastapi import FastAPI\napp = FastAPI()", Lines: 2, IsNew: true}},
	})
	return m
}
