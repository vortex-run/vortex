package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCompat_StatePaths pins where existing installs keep their state. A
// rename (VORTEX → Gubern) or a move out of the cache directory must migrate
// these paths, not silently start using new ones: master.key decrypts the
// secrets, and the audit chain is keyed from it. Update this test only
// together with a tested migration (see the Gubern migration design).
func TestCompat_StatePaths(t *testing.T) {
	for _, env := range []string{"VORTEX_MASTER_KEY_FILE", "VORTEX_AUDIT_LOG",
		"VORTEX_SECRET_STORE", "VORTEX_PLUGIN_DIR", "VORTEX_NAMESPACE_STORE"} {
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
		{"master key", masterKeyPath(), filepath.Join(cfg, "vortex", "master.key")},
		{"TUI key", tuiKeyPath(), filepath.Join(cfg, "vortex", "tui-key")},
		{"AI provider config", aiProviderConfigPath(), filepath.Join(cfg, "vortex", "ai-provider.json")},
		{"audit log", auditLogPath(), filepath.Join(cache, "vortex", "audit.log")},
		{"secret store", secretStorePath(), filepath.Join(cache, "vortex", "secrets")},
		{"key slots", keystorePath(), filepath.Join(cache, "vortex", "keyslots.db")},
		{"plugins", pluginStorePath(), filepath.Join(cache, "vortex", "plugins")},
		{"namespaces", namespaceStorePath(), filepath.Join(cache, "vortex", "namespaces.json")},
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
	t.Setenv("VORTEX_AUDIT_LOG", filepath.Join(dir, "a.log"))
	t.Setenv("VORTEX_MASTER_KEY_FILE", filepath.Join(dir, "m.key"))
	if auditLogPath() != filepath.Join(dir, "a.log") || masterKeyPath() != filepath.Join(dir, "m.key") {
		t.Errorf("overrides ignored: audit=%q master=%q", auditLogPath(), masterKeyPath())
	}
}
