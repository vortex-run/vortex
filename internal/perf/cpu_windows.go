//go:build windows

package perf

import "syscall"

// processCPUSeconds returns the cumulative CPU time (user + kernel) consumed
// by this process, in seconds.
func processCPUSeconds() (float64, bool) {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, false
	}
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, false
	}
	return float64(kernel.Nanoseconds()+user.Nanoseconds()) / 1e9, true
}
