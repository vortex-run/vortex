//go:build integration && windows

package testutil

import (
	"os/exec"
	"testing"
	"time"
)

// stopProcess invokes the `stop` subcommand (Windows has no SIGTERM), which
// posts to the localhost-only /internal/shutdown endpoint, then waits up to 10s
// for the process to exit.
func stopProcess(t *testing.T, p *VortexProcess) {
	t.Helper()
	stop := exec.Command(p.BinaryPath, "stop")
	if out, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("vortex stop: %v\n%s", err, out)
	}
	waitExit(t, p, 10*time.Second)
}

// waitExit waits for the process to exit within d, failing the test otherwise.
func waitExit(t *testing.T, p *VortexProcess, d time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.Cmd.Wait() }()
	select {
	case <-done:
		// Graceful shutdown via the stop command; process exit observed.
	case <-time.After(d):
		_ = p.Cmd.Process.Kill()
		t.Fatalf("vortex did not exit within %s", d)
	}
}
