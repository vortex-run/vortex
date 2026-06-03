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
