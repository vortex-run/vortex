package agents

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestBoundedBuffer_KeepsHeadAndCountsDropped(t *testing.T) {
	b := &boundedBuffer{Limit: 10}
	n, err := b.Write([]byte("0123456789ABCDEF"))
	if err != nil {
		t.Fatalf("Write returned an error: %v", err)
	}
	// The full length must be reported: a short write makes exec.Cmd kill the
	// command with an I/O error, turning "too much output" into a confusing
	// command failure.
	if n != 16 {
		t.Errorf("Write returned n = %d, want 16 (the full input)", n)
	}
	if !b.Truncated() {
		t.Error("Truncated() = false after overflowing the limit")
	}
	got := b.String()
	if !strings.HasPrefix(got, "0123456789") {
		t.Errorf("kept %q, want the first 10 bytes (the head is where errors appear)", got)
	}
	if !strings.Contains(got, "6 more bytes") {
		t.Errorf("truncation notice missing or wrong: %q", got)
	}
}

func TestBoundedBuffer_UnderLimitIsUntouched(t *testing.T) {
	b := &boundedBuffer{Limit: 100}
	_, _ = b.Write([]byte("hello"))
	if b.Truncated() {
		t.Error("Truncated() = true for output well under the limit")
	}
	if b.String() != "hello" {
		t.Errorf("String() = %q, want the exact output with no marker", b.String())
	}
}

func TestBoundedBuffer_ManyWritesAccumulateToTheLimit(t *testing.T) {
	b := &boundedBuffer{Limit: 50}
	for i := 0; i < 100; i++ {
		_, _ = b.Write([]byte("0123456789"))
	}
	// 1000 bytes written, 50 retained.
	body := strings.SplitN(b.String(), "\n... [output truncated", 2)[0]
	if len(body) != 50 {
		t.Errorf("retained %d bytes, want exactly the 50-byte limit", len(body))
	}
	if !strings.Contains(b.String(), "950 more bytes") {
		t.Errorf("dropped count wrong: %q", b.String())
	}
}

func TestBoundedBuffer_ConcurrentWritesAreSafe(t *testing.T) {
	// exec.Cmd writes stdout and stderr from separate goroutines, and a caller
	// may read while the command still runs, so the buffer must be race-free.
	b := &boundedBuffer{Limit: 1000}
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				_, _ = b.Write([]byte("xxxxx"))
				_ = b.String()
			}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	if !b.Truncated() {
		t.Error("expected truncation after 4000 bytes into a 1000-byte buffer")
	}
}

// TestRunCommand_OutputIsBounded is the regression that matters: a command
// producing far more output than the limit must not grow the process heap in
// proportion. Before this, stdout was captured into an unbounded bytes.Buffer,
// so an approved command could exhaust memory well inside the timeout.
func TestRunCommand_OutputIsBounded(t *testing.T) {
	var cmd, arg string
	if runtime.GOOS == "windows" {
		cmd, arg = "cmd", "/c"
	} else {
		cmd, arg = "sh", "-c"
	}
	// Emit a few MB — comfortably over the 1 MiB cap, quick on every platform.
	script := "for i in $(seq 1 60000); do echo AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA; done"
	if runtime.GOOS == "windows" {
		script = "for /L %i in (1,1,60000) do @echo AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	}

	tool := RunCommandTool{
		SandboxDir:      t.TempDir(),
		AllowedCommands: []string{cmd},
		approved:        true,
	}
	res, err := tool.Execute(context.Background(), map[string]any{
		"command": cmd, "args": []string{arg, script},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.(map[string]any)
	stdout := out["stdout"].(string)

	// The retained output must be bounded, not proportional to what was
	// produced. Allow headroom for the truncation notice itself.
	if len(stdout) > maxCommandOutputBytes+512 {
		t.Errorf("captured %d bytes, want <= the %d limit — output is still unbounded",
			len(stdout), maxCommandOutputBytes)
	}
	if !strings.Contains(stdout, "output truncated") {
		t.Errorf("expected a truncation marker so the caller knows output was cut; got %d bytes ending %q",
			len(stdout), stdout[max(0, len(stdout)-80):])
	}
}
