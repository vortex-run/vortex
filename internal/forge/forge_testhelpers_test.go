package forge

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile writes content to dir/name, creating parent directories, failing
// the test on error. Shared across forge tests.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
