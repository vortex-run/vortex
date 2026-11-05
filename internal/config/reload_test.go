package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestManager_LastReloadError: the outcome of the latest Reload is readable
// (the lifecycle hook swallows it).
func TestManager_LastReloadError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gubern.cue")
	good := "cluster: { name: \"m\" }\ntls: { provider: \"internal\", acme_email: \"m@example.com\" }\n"
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("cluster: {"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = m.Reload()
	if m.LastReloadError() == nil {
		t.Error("LastReloadError is nil after reloading an invalid file")
	}
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = m.Reload()
	if err := m.LastReloadError(); err != nil {
		t.Errorf("LastReloadError = %v after a good reload, want nil", err)
	}
}
