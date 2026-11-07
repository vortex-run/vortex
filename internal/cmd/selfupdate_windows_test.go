//go:build windows

package cmd

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// runningCopy copies a system binary to dir/name and starts it so the file
// is in use, the way the vortex.exe being updated is.
func runningCopy(t *testing.T, dir, name string) string {
	t.Helper()
	ping := filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE")
	self := filepath.Join(dir, name)
	if err := copyFile(ping, self); err != nil {
		t.Skipf("can't copy %s: %v", ping, err)
	}
	cmd := exec.Command(self, "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	time.Sleep(200 * time.Millisecond)
	return self
}

func writeNewBinary(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "download.exe")
	if err := os.WriteFile(p, []byte("NEW BINARY"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSwapBinary_ReplacesRunningExe: the running binary is renamed aside and
// the new one put in its place, with no manual steps.
func TestSwapBinary_ReplacesRunningExe(t *testing.T) {
	dir := t.TempDir()
	self := runningCopy(t, dir, "vortex.exe")
	orig := verifyBinary
	verifyBinary = func(string) error { return nil }
	t.Cleanup(func() { verifyBinary = orig })

	var out bytes.Buffer
	if err := swapBinary(writeNewBinary(t, dir), self, "v9.9.9", &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(self); string(got) != "NEW BINARY" {
		t.Errorf("binary not replaced: %q", got)
	}
	if _, err := os.Stat(self + ".old"); err != nil {
		t.Errorf("old binary should remain while it's running: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("v9.9.9")) {
		t.Errorf("out = %q", out.String())
	}
}

// TestSwapBinary_RollsBackOnFailedVerification: a new binary that doesn't
// run is removed and the previous one is put back.
func TestSwapBinary_RollsBackOnFailedVerification(t *testing.T) {
	dir := t.TempDir()
	self := runningCopy(t, dir, "vortex.exe")
	before, _ := os.ReadFile(self)
	orig := verifyBinary
	verifyBinary = func(string) error { return errors.New("exit status 1") }
	t.Cleanup(func() { verifyBinary = orig })

	if err := swapBinary(writeNewBinary(t, dir), self, "v9.9.9", &bytes.Buffer{}); err == nil {
		t.Fatal("expected a verification error")
	}
	if got, _ := os.ReadFile(self); !bytes.Equal(got, before) {
		t.Error("previous binary not restored")
	}
	if _, err := os.Stat(self + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".old should be gone after rollback: %v", err)
	}
}

func TestRemoveStaleOldBinary(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "vortex.exe")
	if err := os.WriteFile(self+".old", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeStaleOldBinary(self)
	if _, err := os.Stat(self + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale .old not removed: %v", err)
	}
}
