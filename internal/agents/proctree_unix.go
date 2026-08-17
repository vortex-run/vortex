//go:build !windows

package agents

import (
	"os/exec"
	"syscall"
)

// Process-tree containment (production audit M5).
//
// exec.CommandContext kills only the process it started. A command that spawns
// anything — a shell backgrounding a job, a build tool starting a daemon, a
// server left running — leaves those children alive when the timeout fires, so
// the timeout bounds the tool call but not the work. Verified before writing
// this: a shell told to background a long-running process left it running long
// after the parent was gone.
//
// The fix is to give the child its own process group and signal the whole
// group, so descendants die with it.

// setProcessGroup puts the command in a new process group, making every
// descendant killable in one call. Without it, a kill reaches only the direct
// child and grandchildren are reparented and survive.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessTree terminates the command and every process in its group.
//
// Signalling the negative PID addresses the process GROUP, which is why
// setProcessGroup must have run first — otherwise the negative PID would
// address this server's own group and kill VORTEX along with the command.
// That is also why it refuses to act when the group was never set up, rather
// than falling back to something that could take the server down.
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		// Not group-isolated: kill only the direct child. Killing a group we
		// did not create risks taking down the server itself.
		_ = cmd.Process.Kill()
		return
	}
	pgid := cmd.Process.Pid // equals the group id, since the child led a new group
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		// The group may already be gone (normal exit); fall back to the child.
		_ = cmd.Process.Kill()
	}
}
