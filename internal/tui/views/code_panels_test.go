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
