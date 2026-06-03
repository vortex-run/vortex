//go:build !windows

package pidfile

import (
	"fmt"
	"os"
	"syscall"
)

// acquireLock opens the lock file and takes a non-blocking exclusive flock.
// If another process holds it, syscall.Flock returns EWOULDBLOCK.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening lock file %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock already held on %s: %w", path, err)
	}
	return f, nil
}

// releaseLock releases the flock held on f.
func releaseLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
