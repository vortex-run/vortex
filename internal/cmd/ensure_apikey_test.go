package cmd

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/auth"
)

// setConfigHome points os.UserConfigDir() at dir on both Linux (XDG_CONFIG_HOME)
// and Windows (APPDATA), so tuiKeyPath() resolves under the temp dir on CI and
// locally alike.
func setConfigHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestEnsureAPIKey_AutoCreatesWhenEmpty(t *testing.T) {
	home := t.TempDir()
	setConfigHome(t, home)

	store := auth.NewAPIKeyStore()
	ensureAPIKey(store, quietLogger())

	if store.Count() != 1 {
		t.Fatalf("Count = %d, want 1 auto-created key", store.Count())
	}
	// The raw secret must be written to tui-key and must verify against the store.
	raw, err := os.ReadFile(tuiKeyPath())
	if err != nil {
		t.Fatalf("tui-key not written: %v", err)
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		t.Fatal("tui-key file is empty")
	}
	if _, verr := store.Verify(secret); verr != nil {
		t.Errorf("auto-created tui-key does not verify: %v", verr)
	}
}

func TestEnsureAPIKey_ImportsExistingTuiKey(t *testing.T) {
	home := t.TempDir()
	setConfigHome(t, home)

	// Pre-write a raw key (as `vortex setup` would) issued by a separate store,
	// simulating an empty hash store on this boot.
	issuer := auth.NewAPIKeyStore()
	_, secret, _ := issuer.Issue("admin", "default", []auth.Role{auth.RoleAdmin}, "setup", 0)
	keyPath := tuiKeyPath()
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	store := auth.NewAPIKeyStore()
	ensureAPIKey(store, quietLogger())

	if store.Count() != 1 {
		t.Fatalf("Count = %d, want 1 imported key", store.Count())
	}
	if _, err := store.Verify(secret); err != nil {
		t.Errorf("pre-existing tui-key should verify after import: %v", err)
	}
	// The file must be left untouched (not overwritten by an auto-create).
	after, _ := os.ReadFile(keyPath)
	if strings.TrimSpace(string(after)) != secret {
		t.Error("tui-key file should not be overwritten when importing")
	}
}

func TestEnsureAPIKey_NoopWhenAlreadyPopulated(t *testing.T) {
	home := t.TempDir()
	setConfigHome(t, home)

	store := auth.NewAPIKeyStore()
	_, _, _ = store.Issue("existing", "default", []auth.Role{auth.RoleOperator}, "boot", 0)

	ensureAPIKey(store, quietLogger())

	if store.Count() != 1 {
		t.Errorf("Count = %d, want 1 (no extra key created)", store.Count())
	}
	// No tui-key should be written when a key already exists and no tui-key file
	// was present.
	if _, err := os.Stat(tuiKeyPath()); err == nil {
		t.Error("ensureAPIKey should not write tui-key when the store is already populated")
	}
}
