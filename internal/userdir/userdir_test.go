package userdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearUserDirEnv removes what every OS derives the user dirs from: HOME and
// XDG_* (Linux, BSD, macOS), APPDATA and LOCALAPPDATA (Windows).
func clearUserDirEnv(t *testing.T) {
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "APPDATA", "LOCALAPPDATA", "home"} {
		t.Setenv(k, "")
	}
}

// TestNoUserDirs_NeverSharedTemp: without a home, state must not land in the
// shared temp dir (it used to, putting the master key at a fixed /tmp path).
func TestNoUserDirs_NeverSharedTemp(t *testing.T) {
	clearUserDirEnv(t)
	if err := Check(); err == nil {
		t.Fatal("Check() = nil with no HOME/APPDATA; stateful commands would run")
	}
	tmp := os.TempDir()
	for name, dir := range map[string]string{"Config": Config(), "Cache": Cache()} {
		if dir != "" {
			t.Errorf("%s() = %q with no user dirs, want \"\" (working directory)", name, dir)
		}
		if p := filepath.Join(dir, "vortex", "master.key"); strings.HasPrefix(p, tmp) {
			t.Errorf("%s-based path %q is in the shared temp dir", name, p)
		}
	}
}

func TestWithUserDirs(t *testing.T) {
	if err := Check(); err != nil {
		t.Skipf("this environment has no user dirs: %v", err)
	}
	if Config() == "" || Cache() == "" {
		t.Errorf("Config() = %q, Cache() = %q; want both set", Config(), Cache())
	}
}
