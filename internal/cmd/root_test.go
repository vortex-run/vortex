package cmd

import (
	"io"
	"log/slog"
	"testing"
)

// ensureTestLogger initialises the package-level logger for tests that call
// command logic directly (bypassing PersistentPreRunE). Output is discarded.
func ensureTestLogger() {
	log = slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewRootCommandInitialises(t *testing.T) {
	root := NewRootCommand()
	if root == nil {
		t.Fatal("NewRootCommand returned nil")
	}
	if root.Use != "vortex" {
		t.Errorf("root.Use = %q, want vortex", root.Use)
	}
}

func TestPersistentFlagsRegistered(t *testing.T) {
	root := NewRootCommand()
	for _, name := range []string{"config", "log-level", "json"} {
		if f := root.PersistentFlags().Lookup(name); f == nil {
			t.Errorf("persistent flag --%s is not registered", name)
		}
	}
}

func TestConfigFlagDefault(t *testing.T) {
	root := NewRootCommand()
	f := root.PersistentFlags().Lookup("config")
	if f == nil {
		t.Fatal("--config not registered")
	}
	if f.DefValue != "vortex.cue" {
		t.Errorf("--config default = %q, want vortex.cue", f.DefValue)
	}
}

func TestLogLevelFlagDefault(t *testing.T) {
	root := NewRootCommand()
	f := root.PersistentFlags().Lookup("log-level")
	if f == nil {
		t.Fatal("--log-level not registered")
	}
	if f.DefValue != "info" {
		t.Errorf("--log-level default = %q, want info", f.DefValue)
	}
}

func TestJSONFlagDefault(t *testing.T) {
	root := NewRootCommand()
	f := root.PersistentFlags().Lookup("json")
	if f == nil {
		t.Fatal("--json not registered")
	}
	if f.DefValue != "false" {
		t.Errorf("--json default = %q, want false", f.DefValue)
	}
}
