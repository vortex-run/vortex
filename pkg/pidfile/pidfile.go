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
	"time"
)

// Write records the current process ID in the file at path, creating parent
// directories as needed. Creation is atomic via O_CREATE|O_EXCL, eliminating
// the check-then-write race: if the file already exists, Write inspects the PID
// inside it. If that process is alive, Write returns an error naming the PID;
// if it is dead (a stale lock from a crash), the stale file is removed and the
// write is retried once.
func Write(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating pidfile directory: %w", err)
		}
	}

	if err := writeExcl(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}

	// The file exists. Deciding whether it is a live owner or a stale leftover
	// is a read-modify-write that must be serialized: take the exclusive lock so
	// concurrent writers (goroutines or processes) cannot both clean up and
	// rewrite. If the lock is contended, another writer is acting now — treat
	// this as "someone else owns it".
	lock, lerr := Lock(path)
	if lerr != nil {
		if pid, rerr := Read(path); rerr == nil {
			return fmt.Errorf("vortex is already running (pid %d)", pid)
		}
		return fmt.Errorf("pidfile %s is contended: %w", path, lerr)
	}
	defer func() { _ = lock.Unlock() }()

	// Re-attempt the atomic create under the lock: the previous holder may have
	// already finished and released, or the file may now be stale.
	if err := writeExcl(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}

	// A racing winner may have created the exclusive marker but not yet renamed
	// the PID content into place; a parse failure here can be transient, so give
	// the content a brief moment to appear before concluding the file is stale.
	for attempt := 0; attempt < 50; attempt++ {
		alive, pid, rerr := IsRunning(path)
		if rerr == nil {
			if alive {
				return fmt.Errorf("vortex is already running (pid %d)", pid)
			}
			break // readable and dead → genuinely stale
		}
		if errors.Is(rerr, os.ErrNotExist) {
			break // winner's content never landed; safe to claim
		}
		time.Sleep(time.Millisecond)
	}

	// Stale (dead PID, unreadable, or invalid): remove and retry once.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing stale pidfile %s: %w", path, err)
	}
	if err := writeExcl(path); err != nil {
		return fmt.Errorf("writing pidfile %s after stale cleanup: %w", path, err)
	}
	return nil
}

// writeExcl claims path for this process and writes the current PID, ensuring
// the pidfile is never observed empty by a racing reader. It first creates an
// exclusive marker at path (O_CREATE|O_EXCL) to win the claim atomically — this
// returns an error wrapping os.ErrExist if another writer already holds it —
// then writes the PID to a temp file and renames it over path, so the content
// appears atomically and fully.
func writeExcl(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_ = f.Close()

	tmp, err := os.CreateTemp(filepath.Dir(path), ".vortex-pid-*")
	if err != nil {
		return fmt.Errorf("creating temp pidfile: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing pidfile %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing temp pidfile: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("finalizing pidfile %s: %w", path, err)
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

// FileLock is an advisory exclusive lock held on a lock file adjacent to the
// pidfile. It prevents two `vortex start` processes from racing to claim the
// same pidfile. Release it with Unlock. (Named FileLock rather than Lock so the
// Lock acquisition function can keep that name.)
type FileLock struct {
	path string
	file *os.File
}

// lockPath returns the lock-file path companion to a pidfile path.
func lockPath(pidPath string) string { return pidPath + ".lock" }

// Lock acquires an exclusive lock keyed to the pidfile at path. The underlying
// mechanism is OS-specific (flock on Unix, exclusive open on Windows). It
// blocks behavior is non-blocking: if another process holds the lock, it
// returns an error rather than waiting.
func Lock(path string) (*FileLock, error) {
	lp := lockPath(path)
	if dir := filepath.Dir(lp); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating lock directory: %w", err)
		}
	}
	f, err := acquireLock(lp)
	if err != nil {
		return nil, err
	}
	return &FileLock{path: lp, file: f}, nil
}

// Unlock releases the lock and removes the lock file. It is safe to call once.
func (l *FileLock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := releaseLock(l.file)
	_ = l.file.Close()
	_ = os.Remove(l.path)
	l.file = nil
	return err
}

// WriteLocked acquires the exclusive lock and then writes the pidfile while
// holding it, so the check-and-write is atomic against other processes. The
// returned Lock must be released by the caller (typically with defer
// lock.Unlock()) when the server shuts down.
func WriteLocked(path string) (*FileLock, error) {
	lock, err := Lock(path)
	if err != nil {
		return nil, err
	}
	if err := Write(path); err != nil {
		_ = lock.Unlock()
		return nil, err
	}
	return lock, nil
}
