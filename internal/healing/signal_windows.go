//go:build windows

package healing

import "os"

// signalZero treats a successfully-found process as alive on Windows, where
// os.FindProcess returns an error for a non-existent PID.
func signalZero(_ *os.Process) error {
	return nil
}
