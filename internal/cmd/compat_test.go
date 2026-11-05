package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vortex-run/vortex/internal/userdir"
)

// TestCompat_StatePaths pins where existing installs keep their state. A
// rename (VORTEX → Gubern) or a move out of the cache directory must migrate
// these paths, not silently start using new ones: master.key decrypts the
// secrets, and the audit chain is keyed from it. Update this test only
// together with a tested migration (see the Gubern migration design).
func TestCompat_StatePaths(t *testing.T) {
	for _, env := range []string{"GUBERN_MASTER_KEY_FILE", "GUBERN_AUDIT_LOG",
		"GUBERN_SECRET_STORE", "GUBERN_PLUGIN_DIR", "GUBERN_NAMESPACE_STORE"} {
		t.Setenv(env, "")
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Skip("no user config dir on this platform")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no user cache dir on this platform")
	}
	cases := []struct {
		name, got, want string
	}{
		{"master key", masterKeyPath(), filepath.Join(userdir.App(cfg), "master.key")},
		{"TUI key", tuiKeyPath(), filepath.Join(userdir.App(cfg), "tui-key")},
		{"AI provider config", aiProviderConfigPath(), filepath.Join(userdir.App(cfg), "ai-provider.json")},
		{"audit log", auditLogPath(), filepath.Join(userdir.App(cache), "audit.log")},
		{"secret store", secretStorePath(), filepath.Join(userdir.App(cache), "secrets")},
		{"key slots", keystorePath(), filepath.Join(userdir.App(cache), "keyslots.db")},
		{"plugins", pluginStorePath(), filepath.Join(userdir.App(cache), "plugins")},
		{"namespaces", namespaceStorePath(), filepath.Join(userdir.App(cache), "namespaces.json")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s path = %q, want %q (existing installs keep their state there)", c.name, c.got, c.want)
		}
	}
}

// The override variables operators already use must keep working.
func TestCompat_StatePathOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GUBERN_AUDIT_LOG", filepath.Join(dir, "a.log"))
	t.Setenv("GUBERN_MASTER_KEY_FILE", filepath.Join(dir, "m.key"))
	if auditLogPath() != filepath.Join(dir, "a.log") || masterKeyPath() != filepath.Join(dir, "m.key") {
		t.Errorf("overrides ignored: audit=%q master=%q", auditLogPath(), masterKeyPath())
	}
}
