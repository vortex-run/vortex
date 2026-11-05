package cmd

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestStartupProgress_ReportsTheSlowStepOnce: a step that runs past the
// threshold is named once; a fast one isn't mentioned.
func TestStartupProgress_ReportsTheSlowStepOnce(t *testing.T) {
	old := slowStartupStep
	slowStartupStep = 200 * time.Millisecond
	t.Cleanup(func() { slowStartupStep = old })

	var buf lockedBuf
	p := &startupProgress{}
	p.begin(slog.New(slog.NewTextHandler(&buf, nil)))
	p.step("fast step")
	p.step("slow step")
	time.Sleep(1500 * time.Millisecond)
	p.finish()

	out := buf.String()
	if strings.Count(out, `step="slow step"`) != 1 {
		t.Errorf("slow step should be reported once:\n%s", out)
	}
	if strings.Contains(out, "fast step") {
		t.Errorf("fast step reported:\n%s", out)
	}
}
