//go:build unix

package perf

import "syscall"

// processCPUSeconds returns the cumulative CPU time (user + system) consumed
// by this process, in seconds.
func processCPUSeconds() (float64, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	return float64(ru.Utime.Nano()+ru.Stime.Nano()) / 1e9, true
}
