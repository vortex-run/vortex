// Package pidfile manages VORTEX's PID file: a small text file holding the
// running server's process ID. It is used to coordinate the start/stop/status/
// reload CLI commands and to prevent two servers from running against the same
// pidfile.
//
// Writing is stale-lock aware: if the pidfile already names a process that is
// still alive, Write refuses; if it names a dead process (a stale lock left by
// a crash), Write silently takes over. Liveness probing is implemented per-OS
// (see pidfile_unix.go / pidfile_windows.go) so it works on both.
package pidfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Write records the current process ID in the file at path, creating parent
// directories as needed. If path already names a live process it returns an
// error; if it names a dead process the stale file is overwritten.
func Write(path string) error {
	if alive, pid, err := IsRunning(path); err == nil && alive {
		return fmt.Errorf("vortex is already running (pid %d)", pid)
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating pidfile directory: %w", err)
		}
	}

	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(path, []byte(pid+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing pidfile %s: %w", path, err)
	}
	return nil
}

// Read returns the PID recorded in the file at path. If the file does not
// exist it returns os.ErrNotExist.
func Read(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, os.ErrNotExist
		}
		return 0, fmt.Errorf("reading pidfile %s: %w", path, err)
	}
	s := strings.TrimSpace(string(b))
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("pidfile %s contains invalid PID %q: %w", path, s, err)
	}
	return pid, nil
}

// Remove deletes the pidfile. It is idempotent: a missing file is not an error.
func Remove(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing pidfile %s: %w", path, err)
	}
	return nil
}

// IsRunning reports whether the process named by the pidfile at path is alive.
// It returns (alive, pid, err). A missing pidfile is reported as
// (false, 0, os.ErrNotExist).
func IsRunning(path string) (bool, int, error) {
	pid, err := Read(path)
	if err != nil {
		return false, 0, err
	}
	return processAlive(pid), pid, nil
}
