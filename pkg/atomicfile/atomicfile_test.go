package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrite_CreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.json")
	if err := Write(path, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"a":1}` {
		t.Errorf("content = %q", b)
	}
}

func TestWrite_ReplacesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "new" {
		t.Errorf("content = %q, want new", b)
	}
}

func TestWrite_LeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	for i := 0; i < 5; i++ {
		if err := Write(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("expected only the target file, found %d entries", len(entries))
	}
}

func TestWrite_FailureLeavesTargetIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	if err := os.WriteFile(path, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Writing into a nonexistent directory fails before touching the target.
	bad := filepath.Join(dir, "missing-subdir", "out.json")
	if err := Write(bad, []byte("x"), 0o600); err == nil {
		t.Error("expected error for missing directory")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "precious" {
		t.Errorf("target was modified on unrelated failure: %q", b)
	}
}
