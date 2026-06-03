//go:build windows

package cmd

import (
	"fmt"
	"io"
	"os"
)

// swapBinary cannot replace a running executable on Windows, so it writes the
// new binary alongside the current one as self+".new" and prints the manual
// steps to finish the update.
func swapBinary(newBin, self, newVersion string, out io.Writer) error {
	dst := self + ".new"
	if err := copyFile(newBin, dst); err != nil {
		return fmt.Errorf("writing new binary: %w", err)
	}
	fmt.Fprintf(out, "Download complete (%s). To finish updating on Windows:\n", newVersion)
	fmt.Fprintln(out, "  1. Stop VORTEX: vortex stop")
	fmt.Fprintf(out, "  2. Run: move %s %s\n", dst, self)
	fmt.Fprintln(out, "  3. Start VORTEX: vortex start")
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
