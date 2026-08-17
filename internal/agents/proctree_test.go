package agents

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// longRunningChild returns a command that a shell will run as a normal CHILD
// process, and a way to ask the OS whether it is still alive.
//
// This is the realistic orphan shape — vortex → shell → child. Killing only
// the shell (what exec.CommandContext does) leaves the child running. It is
// deliberately NOT a detached process (`start /b` on Windows, `setsid` on
// Unix): those sever the parent link on purpose and no tree-walk can follow
// them, so testing with one would measure the wrong thing.
func longRunningChild(t *testing.T) (command string, alive func() bool, cleanup func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return "ping -n 60 127.0.0.1",
			func() bool {
				out, _ := exec.Command("tasklist", "/FI", "IMAGENAME eq PING.EXE").CombinedOutput()
				return strings.Contains(strings.ToUpper(string(out)), "PING.EXE")
			},
			func() { _ = exec.Command("taskkill", "/F", "/IM", "ping.exe").Run() }
	}
	return "sleep 300",
		func() bool {
			out, _ := exec.Command("pgrep", "-f", "sleep 300").CombinedOutput()
			return len(strings.TrimSpace(string(out))) > 0
		},
		func() { _ = exec.Command("pkill", "-f", "sleep 300").Run() }
}

// TestRunTerminal_TimeoutKillsChildProcess is the regression for the
// containment gap: exec.CommandContext kills only the process it started, so
// the shell died on timeout while the process it launched kept running. The
// tool's timeout then bounded the CALL but not the WORK, and repeated agent
// runs accumulated stray processes on the host.
func TestRunTerminal_TimeoutKillsChildProcess(t *testing.T) {
	command, alive, cleanup := longRunningChild(t)
	if alive() {
		t.Skip("a matching process is already running; the result could not be attributed")
	}
	t.Cleanup(cleanup)

	tool := RunTerminalTool{cfg: LocalFSConfig{Root: t.TempDir()}, approved: true}
	res, err := tool.Execute(context.Background(), map[string]any{
		"command": command,
		"timeout": float64(2), // the child would otherwise run for 60s+
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Sanity: the command must actually have run, or this test proves nothing.
	// (An earlier version of it passed vacuously because the command never
	// started, so the "no orphan" check was trivially true.)
	if res == nil {
		t.Fatal("no result: the command did not run, so the assertion below is vacuous")
	}

	waitGone(t, alive)
}

// TestRunCommand_TimeoutKillsChildProcess covers the sandboxed tool.
func TestRunCommand_TimeoutKillsChildProcess(t *testing.T) {
	_, alive, cleanup := longRunningChild(t)
	if alive() {
		t.Skip("a matching process is already running; the result could not be attributed")
	}
	t.Cleanup(cleanup)

	shell, args := "sh", []string{"-c", "sleep 300"}
	if runtime.GOOS == "windows" {
		shell, args = "cmd", []string{"/c", "ping", "-n", "60", "127.0.0.1"}
	}
	if _, err := exec.LookPath(shell); err != nil {
		t.Skipf("%s not available", shell)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tool := RunCommandTool{
		SandboxDir:      t.TempDir(),
		AllowedCommands: []string{shell},
		approved:        true,
	}
	if _, err := tool.Execute(ctx, map[string]any{"command": shell, "args": args}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	waitGone(t, alive)
}

// waitGone polls for the child to disappear. Termination is asynchronous —
// the OS reaps on its own schedule — so a single immediate check would be
// racy in both directions.
func waitGone(t *testing.T, alive func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !alive() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("a process started by the command outlived the timeout: " +
		"the timeout bounds the tool call but not the work it started")
}

// TestKillProcessTree_NoProcessIsSafe guards the nil path: killProcessTree is
// called from a cancellation hook that can fire before the process exists.
func TestKillProcessTree_NoProcessIsSafe(_ *testing.T) {
	killProcessTree(exec.Command("does-not-matter")) // must not panic
}

// TestSetProcessGroup_IsApplied checks the attribute is actually set. The Unix
// kill path refuses to signal a group it did not create, so a silently skipped
// setProcessGroup would disable containment without failing anything.
func TestSetProcessGroup_IsApplied(t *testing.T) {
	cmd := exec.Command("echo")
	setProcessGroup(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr not set; process-group isolation is not applied")
	}
}
