//go:build windows

package pidfile

import (
	"fmt"
	"os"
)

// acquireLock takes an exclusive lock on Windows by atomically creating the
// lock file with O_CREATE|O_EXCL. While the file exists, a second caller's
// create fails — giving the same mutual-exclusion guarantee as flock for the
// VORTEX single-instance use case, without depending on golang.org/x/sys.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock already held on %s: %w", path, err)
	}
	return f, nil
}

// releaseLock is a no-op on Windows; the lock is released when Unlock closes
// and removes the file.
func releaseLock(_ *os.File) error {
	return nil
}
