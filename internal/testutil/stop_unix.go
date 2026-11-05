//go:build integration && !windows

package testutil

import (
	"syscall"
	"testing"
	"time"
)

// stopProcess sends SIGTERM and waits up to 10s for the process to exit.
func stopProcess(t *testing.T, p *VortexProcess) {
	t.Helper()
	if err := p.Cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}
	waitExit(t, p, 10*time.Second)
}

// waitExit waits for the process to exit within d, failing the test otherwise.
func waitExit(t *testing.T, p *VortexProcess, d time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.Cmd.Wait() }()
	select {
	case err := <-done:
		// SIGTERM-driven graceful shutdown should exit 0.
		if err != nil {
			t.Fatalf("vortex did not exit cleanly: %v", err)
		}
	case <-time.After(d):
		_ = p.Cmd.Process.Kill()
		t.Fatalf("vortex did not exit within %s", d)
	}
}

// dumpStacks asks a hung child for its goroutine stacks (the Go runtime
// prints them to stderr on SIGQUIT, then exits), so a startup timeout shows
// where it was stuck instead of an empty or truncated log.
func dumpStacks(p *VortexProcess) {
	if p.Cmd == nil || p.Cmd.Process == nil {
		return
	}
	if err := p.Cmd.Process.Signal(syscall.SIGQUIT); err != nil {
		return
	}
	done := make(chan struct{})
	go func() { _ = p.Cmd.Wait(); close(done) }() // Cmd.Wait also drains the output copy
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	p.stopped = true // reaped here; the cleanup must not wait again
}
