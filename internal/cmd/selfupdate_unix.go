//go:build !windows

package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

// swapBinary atomically replaces the running binary at self with newBin on
// Unix: the current binary is moved aside to self+".bak", newBin is copied into
// place and made executable, then the new binary is verified by running it with
// --version. On verification failure the .bak is restored.
func swapBinary(newBin, self, newVersion string, out io.Writer) error {
	bak := self + ".bak"
	if err := os.Rename(self, bak); err != nil {
		return fmt.Errorf("moving current binary aside: %w", err)
	}

	if err := copyFile(newBin, self, 0o755); err != nil {
		_ = os.Rename(bak, self) // restore
		return fmt.Errorf("installing new binary: %w", err)
	}

	if err := exec.Command(self, "--version").Run(); err != nil {
		_ = os.Remove(self)
		_ = os.Rename(bak, self)
		return fmt.Errorf("new binary failed verification, rolled back to previous version: %w", err)
	}

	_ = os.Remove(bak)
	fmt.Fprintf(out, "VORTEX updated to %s\n", newVersion)
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
