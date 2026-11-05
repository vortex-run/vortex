// Package userdir resolves the per-user directories Gubern keeps its state
// in: the config dir (master key, API keys, the TUI key) and the cache dir
// (secret store, audit log, certificates, backups).
//
// When the OS can't name them (HOME unset, as under some service managers),
// the old code fell back to os.TempDir(). On Linux and macOS that is shared
// and world-writable, so the master key and an admin API key would have
// landed at a fixed path any local user could pre-create or read. These
// helpers fall back to the working directory instead, and the CLI refuses to
// run stateful commands at all in that case (see Check).
package userdir

import (
	"fmt"
	"os"
	"path/filepath"
)

// Config returns the user's config directory, or "" (the working directory
// once joined) when the OS can't determine one.
func Config() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return dir
}

// Cache returns the user's cache directory, or "" (the working directory
// once joined) when the OS can't determine one.
func Cache() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return dir
}

// Check reports an error when either directory can't be determined, for
// commands to refuse to start rather than keep secrets somewhere unexpected.
func Check() error {
	if _, err := os.UserConfigDir(); err != nil {
		return fmt.Errorf("can't find a private config directory (%w); set HOME, or XDG_CONFIG_HOME and XDG_CACHE_HOME", err)
	}
	if _, err := os.UserCacheDir(); err != nil {
		return fmt.Errorf("can't find a private cache directory (%w); set HOME, or XDG_CACHE_HOME", err)
	}
	return nil
}

// Name is the per-user directory name for the app's files; LegacyName is the
// pre-rename one (the product was renamed from VORTEX to Gubern).
const (
	Name       = "gubern"
	LegacyName = "vortex"
)

// App returns the app's directory under base (a config or cache dir):
// base/gubern, unless only a pre-rename base/vortex exists, in which case
// that one keeps being used so existing keys, audit logs and databases are
// found where they are. Nothing is moved.
func App(base string) string {
	cur := filepath.Join(base, Name)
	if _, err := os.Stat(cur); err == nil {
		return cur
	}
	legacy := filepath.Join(base, LegacyName)
	if fi, err := os.Stat(legacy); err == nil && fi.IsDir() {
		return legacy
	}
	return cur
}
