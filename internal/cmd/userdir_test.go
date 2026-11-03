package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestRoot_RefusesStatefulCommandsWithoutUserDirs: with no HOME/APPDATA the
// CLI stops before touching keys or secrets (it used to keep them under the
// shared temp dir), while commands that only print still work.
func TestRoot_RefusesStatefulCommandsWithoutUserDirs(t *testing.T) {
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "APPDATA", "LOCALAPPDATA", "home"} {
		t.Setenv(k, "")
	}
	run := func(args ...string) error {
		root := NewRootCommand()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(args)
		return root.Execute()
	}

	for _, args := range [][]string{{"secret", "list"}, {"keys", "list"}, {"audit", "verify"}} {
		err := run(args...)
		if err == nil || !strings.Contains(err.Error(), "private config directory") {
			t.Errorf("vortex %s: err = %v, want a refusal naming the missing config directory", strings.Join(args, " "), err)
		}
	}
	if err := run("version"); err != nil {
		t.Errorf("vortex version: %v, want it to work without user dirs", err)
	}
}
