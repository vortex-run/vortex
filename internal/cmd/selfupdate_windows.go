//go:build windows

package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// verifyBinary checks a freshly installed binary runs. A variable so tests
// can exercise both outcomes without a real release.
var verifyBinary = func(path string) error {
	// The version subcommand exists in every release.
	return exec.Command(path, "version").Run() //nolint:gosec // our own installed binary
}

// swapBinary replaces the running executable on Windows. Windows won't let a
// running .exe be overwritten or deleted, but it can be renamed: the current
// binary moves to self+".old", the new one takes its place, and it is
// verified by running its version command. On failure the old binary is put
// back. The .old file stays until it is no longer running and is removed on
// a later run (removeStaleOldBinary). Before this, the update was left as
// self+".new" with manual move steps.
func swapBinary(newBin, self, newVersion string, out io.Writer) error {
	old := self + ".old"
	// A leftover from an earlier update: it must go before the rename (rename
	// onto an existing file fails on Windows). If it's still running, stop.
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("a previous update's %s is still in use; stop running VORTEX processes and retry: %w", old, err)
	}
	if err := os.Rename(self, old); err != nil {
		return fmt.Errorf("moving the current binary aside: %w", err)
	}
	restore := func() error {
		_ = os.Remove(self)
		return os.Rename(old, self)
	}
	if err := copyFile(newBin, self); err != nil {
		if rerr := restore(); rerr != nil {
			return fmt.Errorf("writing new binary: %w (and restoring the old one failed: %v; it is at %s)", err, rerr, old)
		}
		return fmt.Errorf("writing new binary: %w", err)
	}
	if err := verifyBinary(self); err != nil {
		if rerr := restore(); rerr != nil {
			return fmt.Errorf("new binary failed verification: %w (and restoring the old one failed: %v; it is at %s)", err, rerr, old)
		}
		return fmt.Errorf("new binary failed verification, kept the previous version: %w", err)
	}
	removeStaleOldBinary(self) // works only if nothing runs the old one
	fmt.Fprintf(out, "VORTEX updated to %s. A running server keeps the old version until restarted (vortex stop, then vortex start).\n", newVersion)
	return nil
}

// removeStaleOldBinary deletes self+".old" left by an update, best effort:
// it fails harmlessly while that old binary is still running.
func removeStaleOldBinary(self string) {
	_ = os.Remove(self + ".old")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // the verified download
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755) //nolint:gosec // our install path
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
