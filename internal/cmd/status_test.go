package cmd

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusCommandRegisters(t *testing.T) {
	if newStatusCommand().Use != "status" {
		t.Error("status command Use should be 'status'")
	}
}

func TestStatusFlagDefaults(t *testing.T) {
	c := newStatusCommand()
	cases := map[string]string{
		"pidfile":  "vortex.pid",
		"api-port": "9090",
		"json":     "false",
	}
	for name, want := range cases {
		f := c.Flags().Lookup(name)
		if f == nil {
			t.Errorf("--%s not registered", name)
			continue
		}
		if f.DefValue != want {
			t.Errorf("--%s default = %q, want %q", name, f.DefValue, want)
		}
	}
}

func TestStatusNotRunningExitsError(t *testing.T) {
	c := newStatusCommand()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	missing := filepath.Join(t.TempDir(), "vortex.pid")
	c.SetArgs([]string{"--pidfile", missing})

	err := c.Execute()
	if !errors.Is(err, errNotRunning) {
		t.Fatalf("expected errNotRunning, got %v", err)
	}
	if !strings.Contains(buf.String(), "not running") {
		t.Errorf("output should say 'not running':\n%s", buf.String())
	}
}
