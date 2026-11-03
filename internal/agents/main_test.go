package agents

import (
	"os"
	"testing"
)

// TestMain keeps undo backups made by any test in this package out of the
// developer's real cache directory.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "vortex-agents-backups-")
	if err != nil {
		panic(err)
	}
	backupRoot = func() (string, error) { return root, nil }
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
