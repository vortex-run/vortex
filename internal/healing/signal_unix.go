//go:build !windows

package healing

import (
	"os"
	"syscall"
)

// signalZero sends signal 0 to test whether the process is alive (Unix).
func signalZero(p *os.Process) error {
	return p.Signal(syscall.Signal(0))
}
