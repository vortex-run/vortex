//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"syscall"
)

// requestReload sends SIGHUP, which VORTEX's lifecycle manager handles by
// re-reading and re-validating the config. apiPort is unused on Unix.
func requestReload(pid int, _ int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGHUP); err != nil {
		return fmt.Errorf("sending SIGHUP to %d: %w", pid, err)
	}
	return nil
}
