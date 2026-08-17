//go:build windows

package agents

import (
	"os/exec"
	"strconv"
	"syscall"
)

// Process-tree containment on Windows (production audit M5). See the shared
// rationale in proctree_unix.go: exec.CommandContext kills only the process it
// started, so anything the command spawned outlives the timeout.
//
// Windows has no process groups in the POSIX sense. The equivalent primitive is
// a Job Object, but the Go standard library does not expose one, and reaching
// for raw syscalls here would add a meaningful amount of unsafe, hard-to-test
// code to a security fix. `taskkill /T` walks the process tree from a PID and
// terminates every descendant, which is the same outcome through a supported
// interface — and it is already how the repo stops process trees elsewhere.

// setProcessGroup gives the child its own process group so a console-level
// signal does not propagate back to the server. Tree termination itself is done
// by PID in killProcessTree, so this only isolates the child.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// killProcessTree terminates the command and every descendant.
//
// /T walks the tree, /F forces termination. Failures are ignored on purpose:
// the usual cause is that the process already exited, and a kill that races a
// normal exit must not turn into a reported error.
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	if err := exec.Command("taskkill", "/T", "/F", "/PID", pid).Run(); err != nil {
		// taskkill could not run (missing, or the process is already gone) —
		// still make sure the direct child is not left behind.
		_ = cmd.Process.Kill()
	}
}
