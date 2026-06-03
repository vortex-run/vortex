//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"syscall"
)

// requestStop sends SIGTERM to the process so VORTEX's lifecycle manager runs
// its graceful shutdown. apiPort is unused on Unix.
func requestStop(pid int, _ int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("sending SIGTERM to %d: %w", pid, err)
	}
	return nil
}
