package cmd

import (
	"os"
	"testing"
)

func TestResolvePidfile_LegacyFallback(t *testing.T) {
	t.Chdir(t.TempDir())
	if got := resolvePidfile(defaultPidfile); got != defaultPidfile {
		t.Errorf("nothing present: %s", got)
	}
	if err := os.WriteFile(legacyPidfile, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolvePidfile(defaultPidfile); got != legacyPidfile {
		t.Errorf("only vortex.pid: %s (a server started before the rename must be found)", got)
	}
	if err := os.WriteFile(defaultPidfile, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolvePidfile(defaultPidfile); got != defaultPidfile {
		t.Errorf("both: %s", got)
	}
	if got := resolvePidfile("custom.pid"); got != "custom.pid" {
		t.Errorf("explicit path changed: %s", got)
	}
}
