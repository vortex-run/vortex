package cmd

import (
	"path/filepath"
	"testing"
)

func TestResolveWorkingDir_HonorsEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VORTEX_WORK_DIR", dir)
	got := resolveWorkingDir()
	want, _ := filepath.Abs(dir)
	if got != want {
		t.Errorf("resolveWorkingDir() = %q, want %q (from VORTEX_WORK_DIR)", got, want)
	}
}

func TestResolveWorkingDir_FallsBackToCwd(t *testing.T) {
	t.Setenv("VORTEX_WORK_DIR", "")
	got := resolveWorkingDir()
	if got == "" || got == "." {
		t.Errorf("resolveWorkingDir() = %q, want the process cwd", got)
	}
}
