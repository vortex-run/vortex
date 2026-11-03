//go:build !windows

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// fakeVortex writes an executable that, like the real binary, succeeds for
// `version` and fails for anything else it doesn't know.
func fakeVortex(t *testing.T, path, tag string) {
	t.Helper()
	script := "#!/bin/sh\n[ \"$1\" = version ] && { echo " + tag + "; exit 0; }\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestSwapBinary_VerifiesWithVersionCommand: the swap's post-install check
// must use a command the binary actually has. It ran `--version`, which the
// binary rejected, so every Unix self-update rolled back.
func TestSwapBinary_VerifiesWithVersionCommand(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "vortex")
	newBin := filepath.Join(dir, "vortex.new")
	fakeVortex(t, self, "old")
	fakeVortex(t, newBin, "new")

	var out bytes.Buffer
	if err := swapBinary(newBin, self, "v9.9.9", &out); err != nil {
		t.Fatalf("swapBinary: %v", err)
	}
	got, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("echo new")) {
		t.Error("self was not replaced by the new binary")
	}
	if _, err := os.Stat(self + ".bak"); !os.IsNotExist(err) {
		t.Error(".bak left behind after a successful update")
	}
}

// TestSwapBinary_RollsBackBrokenBinary: a new binary that fails its version
// check is rolled back.
func TestSwapBinary_RollsBackBrokenBinary(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "vortex")
	newBin := filepath.Join(dir, "vortex.new")
	fakeVortex(t, self, "old")
	if err := os.WriteFile(newBin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := swapBinary(newBin, self, "v9.9.9", &bytes.Buffer{}); err == nil {
		t.Fatal("swapBinary accepted a binary whose version command fails")
	}
	got, _ := os.ReadFile(self)
	if !bytes.Contains(got, []byte("echo old")) {
		t.Error("rollback did not restore the previous binary")
	}
}
