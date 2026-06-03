//go:build !windows

package pidfile

import (
	"os"
	"syscall"
)

// processAlive reports whether a process with the given PID exists and is
// signalable by this process. On Unix, FindProcess always succeeds, so we send
// signal 0 (no signal delivered) and inspect the error: nil or EPERM means the
// process exists; ESRCH means it does not.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM: process exists but we lack permission to signal it — still alive.
	return errorsIsPermission(err)
}

func errorsIsPermission(err error) bool {
	return err == syscall.EPERM
}
