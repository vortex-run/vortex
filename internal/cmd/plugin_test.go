package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestPlugin_CommandRegisters(t *testing.T) {
	c := newPluginCommand()
	if c.Use != "plugin" {
		t.Errorf("Use = %q, want plugin", c.Use)
	}
	names := map[string]bool{}
	for _, sub := range c.Commands() {
		names[sub.Name()] = true
	}
	for _, want := range []string{"list", "install", "remove", "info"} {
		if !names[want] {
			t.Errorf("plugin should have %q subcommand, got %v", want, names)
		}
	}
}

func TestPlugin_InstallRequiresPath(t *testing.T) {
	c := newPluginInstallCommand()
	if err := c.Args(c, []string{}); err == nil {
		t.Error("install should require a path argument")
	}
	if err := c.Args(c, []string{"plugin.wasm"}); err != nil {
		t.Errorf("install with one arg should be valid: %v", err)
	}
}

func TestPlugin_RemoveInfoRegister(t *testing.T) {
	if newPluginRemoveCommand().Use != "remove <name>" {
		t.Error("remove Use should be 'remove <name>'")
	}
	if newPluginInfoCommand().Use != "info <name>" {
		t.Error("info Use should be 'info <name>'")
	}
}

func TestPlugin_ListEmptyRegistry(t *testing.T) {
	t.Setenv("VORTEX_PLUGIN_DIR", t.TempDir())

	c := newPluginCommand()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs([]string{"list"})
	if err := c.Execute(); err != nil {
		t.Fatalf("plugin list: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "no plugins installed") {
		t.Errorf("empty registry list = %q, want 'no plugins installed'", buf.String())
	}
}
