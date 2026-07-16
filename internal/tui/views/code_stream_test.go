package views

import (
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/tui"
)

func TestCodeStream_ChunksRenderLiveThenFinalize(t *testing.T) {
	m := sizedCode()
	m.working = true

	ch := make(chan string, 2)
	ch <- "The answer"
	ch <- " is 42."
	close(ch)

	updated, cmd := m.Update(codeStreamMsg{ch: ch})
	m = updated.(CodeModel)

	// Chunk 1: partial reply renders live in the chat panel.
	updated, cmd = m.Update(cmd())
	m = updated.(CodeModel)
	if m.streamText != "The answer" {
		t.Fatalf("streamText = %q after first chunk", m.streamText)
	}
	if chat := m.renderChat(); !strings.Contains(chat, "The answer") {
		t.Error("chat panel does not render the partial reply")
	}
	if !m.working {
		t.Error("working must stay true while streaming")
	}

	// Chunk 2 accumulates.
	updated, cmd = m.Update(cmd())
	m = updated.(CodeModel)
	if m.streamText != "The answer is 42." {
		t.Fatalf("streamText = %q after second chunk", m.streamText)
	}

	// Stream end: the accumulated text lands as a normal chat line and the
	// live buffer clears.
	done := cmd().(codeChunkMsg)
	if !done.done {
		t.Fatalf("expected done message, got %+v", done)
	}
	updated, _ = m.Update(done)
	m = updated.(CodeModel)
	if m.working {
		t.Error("working should clear when the stream completes")
	}
	if m.streamText != "" {
		t.Errorf("streamText = %q, want cleared", m.streamText)
	}
	var found bool
	for _, line := range m.chat {
		if line.Role == "agent" && line.Content == "The answer is 42." {
			found = true
		}
	}
	if !found {
		t.Errorf("final chat line missing; chat = %+v", m.chat)
	}
}

func TestCodeStream_ConnectionNoticeRoutesToErrorPath(t *testing.T) {
	m := sizedCode()
	m.working = true

	ch := make(chan string, 1)
	ch <- tui.ConnectionErrorMessage
	close(ch)

	updated, cmd := m.Update(codeStreamMsg{ch: ch})
	m = updated.(CodeModel)
	updated, cmd = m.Update(cmd()) // the notice chunk
	m = updated.(CodeModel)
	updated, _ = m.Update(cmd()) // done
	m = updated.(CodeModel)

	if !m.memOffline {
		t.Error("connection notice should mark the server offline")
	}
	var asChat bool
	for _, line := range m.chat {
		if strings.HasPrefix(line.Content, tui.ConnectionErrorPrefix) {
			asChat = true
		}
	}
	if !asChat {
		t.Error("connection notice should land in the chat panel")
	}
}
