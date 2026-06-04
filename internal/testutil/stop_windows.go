//go:build integration && windows

package testutil

import (
	"net/http"
	"testing"
	"time"
)

// stopProcess triggers shutdown via the localhost-only /internal/shutdown
// endpoint (Windows has no SIGTERM), then waits for the child process to exit.
//
// It posts directly rather than shelling out to `vortex stop`: that subcommand
// runs its own liveness poll against the pidfile, which races the test's
// not-yet-reaped child process on Windows and can spuriously time out. Posting
// here and waiting on p.Cmd.Wait observes the real exit.
func stopProcess(t *testing.T, p *VortexProcess) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(apiBase+"/internal/shutdown", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /internal/shutdown: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/internal/shutdown returned %s", resp.Status)
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
		// Graceful shutdown via the endpoint; process exit observed.
	case <-time.After(d):
		_ = p.Cmd.Process.Kill()
		t.Fatalf("vortex did not exit within %s", d)
	}
}
