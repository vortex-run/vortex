package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsTerminal_NullDeviceIsNot: /dev/null (NUL) is a character device on
// Linux and macOS, and it is a systemd service's default stdin; treating it
// as a terminal ran the setup wizard on every service start.
func TestIsTerminal_NullDeviceIsNot(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("no null device: %v", err)
	}
	defer func() { _ = null.Close() }()
	if isTerminal(null) {
		t.Errorf("isTerminal(%s) = true, want false", os.DevNull)
	}

	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if isTerminal(f) {
		t.Error("isTerminal(regular file) = true, want false")
	}
}
