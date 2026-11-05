//go:build !windows

package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/sandeepbollavaram/vortex/internal/update"
)

// swapBinary atomically replaces the running binary at self with newBin on
// Unix using update.AtomicReplace (which keeps a .bak), then verifies the new
// binary by running its version command. On verification failure it rolls back
// from the .bak; on success it removes the .bak.
func swapBinary(newBin, self, newVersion string, out io.Writer) error {
	if err := update.AtomicReplace(newBin, self); err != nil {
		return err
	}

	// The version subcommand exists in every release; --version (also
	// accepted now) didn't before, which made this check fail.
	if err := exec.Command(self, "version").Run(); err != nil {
		_ = update.Rollback(self)
		return fmt.Errorf("new binary failed verification, rolled back to previous version: %w", err)
	}

	_ = os.Remove(self + ".bak")
	fmt.Fprintf(out, "Gubern updated to %s\n", newVersion)
	return nil
}

// removeStaleOldBinary is Windows-only cleanup (see selfupdate_windows.go).
func removeStaleOldBinary(string) {}
