//go:build windows

package pidfile

import "os"

// processAlive reports whether a process with the given PID is running on
// Windows. os.FindProcess opens a handle to the process and fails if no such
// process exists, which is sufficient as a liveness check here.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Releasing the handle we just opened; its successful acquisition already
	// indicates the process exists.
	_ = proc.Release()
	return true
}
