package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestRoot_VersionFlag: `vortex --version` must exit 0. Self-update in
// released binaries (v0.3.0 and earlier) verifies a downloaded binary by
// running it with --version; if the new binary rejects the flag, every
// update from those versions rolls back.
func TestRoot_VersionFlag(t *testing.T) {
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("vortex --version: %v", err)
	}
	if !strings.Contains(out.String(), version) {
		t.Errorf("--version output = %q, want it to contain %q", out.String(), version)
	}
}
