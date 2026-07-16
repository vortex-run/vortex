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

func TestCodeStream_SecondSubmitSupersedesFirst(t *testing.T) {
	m := sizedCode()
	m.working = true

	first := make(chan string, 2)
	first <- "first partial"
	updated, cmd1 := m.Update(codeStreamMsg{ch: first})
	m = updated.(CodeModel)
	updated, cmd1 = m.Update(cmd1()) // consume "first partial"
	m = updated.(CodeModel)

	// The user submits again before the first reply completes.
	second := make(chan string, 2)
	second <- "second reply"
	close(second)
	updated, cmd2 := m.Update(codeStreamMsg{ch: second})
	m = updated.(CodeModel)

	// The first stream's partial text is preserved as a chat line, not lost
	// or interleaved into the new stream's buffer.
	if m.streamText != "" {
		t.Fatalf("streamText = %q, want reset for the new stream", m.streamText)
	}
	var partialKept bool
	for _, line := range m.chat {
		if line.Content == "first partial" {
			partialKept = true
		}
	}
	if !partialKept {
		t.Error("superseded stream's partial text should land as a chat line")
	}

	// Late chunks from the first stream are drained silently.
	first <- " late chunk"
	close(first)
	updated, cmd1 = m.Update(cmd1()) // " late chunk" — stale, must not render
	m = updated.(CodeModel)
	if strings.Contains(m.streamText, "late chunk") {
		t.Errorf("stale chunk leaked into live buffer: %q", m.streamText)
	}
	updated, _ = m.Update(cmd1()) // stale done — must not replay as a reply
	m = updated.(CodeModel)
	for _, line := range m.chat {
		if strings.Contains(line.Content, "late chunk") {
			t.Errorf("stale stream replayed into chat: %q", line.Content)
		}
	}

	// The second stream proceeds normally.
	updated, cmd2 = m.Update(cmd2()) // "second reply"
	m = updated.(CodeModel)
	if m.streamText != "second reply" {
		t.Fatalf("streamText = %q, want second stream's text", m.streamText)
	}
	updated, _ = m.Update(cmd2()) // done
	m = updated.(CodeModel)
	var finalKept bool
	for _, line := range m.chat {
		if line.Content == "second reply" {
			finalKept = true
		}
	}
	if !finalKept {
		t.Errorf("second reply missing from chat; chat = %+v", m.chat)
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
