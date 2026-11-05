package views

import (
	"strings"
	"testing"
)

// feed delivers chunks to the model as one open stream.
func feed(t *testing.T, m AgentsModel, ch chan string, chunks ...string) AgentsModel {
	t.Helper()
	for _, c := range chunks {
		updated, _ := m.Update(agentChunkMsg{ch: ch, chunk: c})
		m = updated.(AgentsModel)
	}
	return m
}

func streaming() (AgentsModel, chan string) {
	m := NewAgents(nil)
	ch := make(chan string)
	m.thinking = true
	m.streamCh = ch
	return m, ch
}

// TestAgents_MidStreamApproval: /agent waits for approval with the stream
// open, so the box and Y/N must come up before the stream ends, and the
// preview that follows must land in the box.
func TestAgents_MidStreamApproval(t *testing.T) {
	m, ch := streaming()
	m = feed(t, m, ch, "→ list_directory\n", "[APPROVAL_REQUIRED|LOW RISK|write_file] Write a.txt (action 0123456789abcdef)\n")
	if !m.Awaiting() || m.approvalAction != "0123456789abcdef" || !m.approvalInBox {
		t.Fatalf("awaiting=%v action=%q inBox=%v", m.Awaiting(), m.approvalAction, m.approvalInBox)
	}
	if m.streamText != "" {
		t.Errorf("approval text should move out of the stream buffer: %q", m.streamText)
	}
	m = feed(t, m, ch, "+ hello world\n")
	box := m.messages[len(m.messages)-1].Content
	if !strings.Contains(box, "+ hello world") || !strings.Contains(box, "Press [Y]") || strings.Contains(box, "[APPROVAL_REQUIRED") {
		t.Errorf("box = %q", box)
	}

	// Approval resolves; later lines stream normally; result isn't duplicated.
	updated, _ := m.Update(approvalResult{approved: true, result: "✓ Action approved"})
	m = updated.(AgentsModel)
	if m.Awaiting() || m.approvalInBox {
		t.Fatal("still awaiting after the decision")
	}
	n := len(m.messages)
	m = feed(t, m, ch, "✓ write_file done\n")
	if len(m.messages) != n || !strings.Contains(m.streamText, "write_file done") {
		t.Errorf("post-approval lines should stream: msgs %d→%d, stream %q", n, len(m.messages), m.streamText)
	}

	// The same action asked again (e.g. after a rejection) gets a new box.
	m = feed(t, m, ch, "[APPROVAL_REQUIRED|LOW RISK|write_file] Write a.txt (action 0123456789abcdef)\n")
	if !m.Awaiting() {
		t.Error("a repeated request for the same action wasn't shown")
	}
}

// TestAgents_StreamEndsWhileAwaiting: a plain tool request ends its stream
// right after the approval line while the action stays pending server-side,
// so the box keeps Y/N live and the decision's result is shown.
func TestAgents_StreamEndsWhileAwaiting(t *testing.T) {
	m, ch := streaming()
	m = feed(t, m, ch, "[APPROVAL_REQUIRED|HIGH RISK|run_terminal] Run ls (action fedcba9876543210)\n", "$ ls\n")
	updated, _ := m.Update(agentChunkMsg{ch: ch, done: true})
	m = updated.(AgentsModel)
	if !m.Awaiting() || m.approvalInBox || m.thinking {
		t.Fatalf("awaiting=%v inBox=%v thinking=%v", m.Awaiting(), m.approvalInBox, m.thinking)
	}
	if m.approvalAction != "fedcba9876543210" {
		t.Errorf("action = %q", m.approvalAction)
	}
	updated, _ = m.Update(approvalResult{approved: true, result: "✓ Completed (exit 0)"})
	m = updated.(AgentsModel)
	if last := m.messages[len(m.messages)-1].Content; !strings.Contains(last, "Completed") {
		t.Errorf("result not shown after the stream ended: %q", last)
	}
}

func TestAutocomplete_AgentCommand(t *testing.T) {
	if got := autocomplete("/ag"); got != "/agent " {
		t.Errorf(`autocomplete("/ag") = %q, want "/agent "`, got)
	}
}
