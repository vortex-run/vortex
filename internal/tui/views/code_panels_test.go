package views

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
