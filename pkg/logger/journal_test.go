//go:build linux

package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestIsJournaldFalseWhenUnset(t *testing.T) {
	t.Setenv("JOURNAL_STREAM", "")
	t.Setenv("INVOCATION_ID", "")
	// /run/systemd/private may exist on CI runners; this assertion holds on the
	// GitHub-hosted ubuntu runner where it does not. Guard accordingly.
	if _, err := os.Stat("/run/systemd/private"); err == nil {
		t.Skip("systemd private socket present on this host")
	}
	if IsJournald() {
		t.Error("IsJournald should be false when no journald signals are set")
	}
}

func TestIsJournaldTrueWithJournalStream(t *testing.T) {
	t.Setenv("JOURNAL_STREAM", "8:12345")
	if !IsJournald() {
		t.Error("IsJournald should be true when JOURNAL_STREAM is set")
	}
}

func TestNewJournalHandlerNonNil(t *testing.T) {
	if NewJournalHandler(slog.LevelInfo) == nil {
		t.Error("NewJournalHandler returned nil on linux")
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what
// was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}

func TestJournalHandlerInfoPriority(t *testing.T) {
	out := captureStderr(t, func() {
		log := slog.New(NewJournalHandler(slog.LevelDebug))
		log.Info("hello")
	})
	if !strings.HasPrefix(out, "<6>hello") {
		t.Errorf("INFO should use <6> prefix, got: %q", out)
	}
}

func TestJournalHandlerErrorPriority(t *testing.T) {
	out := captureStderr(t, func() {
		log := slog.New(NewJournalHandler(slog.LevelDebug))
		log.Error("boom")
	})
	if !strings.HasPrefix(out, "<3>boom") {
		t.Errorf("ERROR should use <3> prefix, got: %q", out)
	}
}

func TestJournalHandlerCorrelationFirst(t *testing.T) {
	out := captureStderr(t, func() {
		log := slog.New(NewJournalHandler(slog.LevelDebug))
		ctx := WithCorrelationID(context.Background(), "abc123")
		log.InfoContext(ctx, "msg", "other", "value")
	})
	cidIdx := strings.Index(out, "correlation_id=abc123")
	otherIdx := strings.Index(out, "other=value")
	if cidIdx < 0 {
		t.Fatalf("correlation_id missing: %q", out)
	}
	if otherIdx >= 0 && cidIdx > otherIdx {
		t.Errorf("correlation_id should appear before other attrs: %q", out)
	}
}
