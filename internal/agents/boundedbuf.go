package agents

import (
	"fmt"
	"sync"
)

// Bounded capture for command output.
//
// Command tools captured stdout and stderr into a plain bytes.Buffer, which
// grows for as long as the command keeps writing. The 30s timeout does not
// help: a command that writes as fast as it can produces megabytes per second
// (measured: 11 MB in 5s from a single `cmd /c` loop on Windows, and `yes` on
// Linux is far quicker), the timeout is caller-supplied and can be raised, and
// several agents can run at once. An approved command therefore had a
// straightforward path to exhausting the server's heap — the same
// memory-exhaustion class as production audit H5, on the execution path.
//
// The cap keeps the HEAD of the stream rather than the tail: the first output
// is where a compiler error, a stack trace, or a usage message appears, and a
// runaway process is usually repeating itself by the end anyway.

// maxCommandOutputBytes caps each of stdout and stderr per command execution.
// 1 MiB is far more than any legible tool output and still small enough that
// the worst case across concurrent agents stays bounded. It matches nothing
// else on purpose: the 8 MB API response cap covers a whole AI reply, which is
// a different thing from one command's console output.
const maxCommandOutputBytes = 1 << 20

// boundedBuffer accumulates up to Limit bytes and discards the rest, recording
// how much was dropped. It satisfies io.Writer so it can be handed to
// exec.Cmd's Stdout/Stderr directly.
//
// It is safe for concurrent use: exec.Cmd writes stdout and stderr from
// separate goroutines, and callers may read the result while the command is
// still running.
type boundedBuffer struct {
	Limit int

	mu      sync.Mutex
	buf     []byte
	dropped int
}

// Write stores what fits and counts the rest as dropped. It never returns a
// short write or an error: reporting one would make exec.Cmd kill the command
// with an I/O error, turning "this produced a lot of output" into a confusing
// failure. Truncation is surfaced in the result instead.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.Limit - len(b.buf)
	if room > 0 {
		if room > len(p) {
			room = len(p)
		}
		b.buf = append(b.buf, p[:room]...)
	}
	if rest := len(p) - room; rest > 0 {
		b.dropped += rest
	}
	return len(p), nil
}

// String returns the captured output, with a trailing marker when anything was
// dropped so a caller (and the model reading the tool result) can tell the
// difference between a command that printed this much and one that was cut off.
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dropped == 0 {
		return string(b.buf)
	}
	return string(b.buf) + fmt.Sprintf(
		"\n... [output truncated: %d more bytes not captured, limit %d]", b.dropped, b.Limit)
}

// Truncated reports whether any output was discarded.
func (b *boundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped > 0
}

// newCommandBuffer returns a buffer bounded at maxCommandOutputBytes.
func newCommandBuffer() *boundedBuffer {
	return &boundedBuffer{Limit: maxCommandOutputBytes}
}
