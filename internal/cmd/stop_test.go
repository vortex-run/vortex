package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStopCommandRegisters(t *testing.T) {
	if newStopCommand().Use != "stop" {
		t.Error("stop command Use should be 'stop'")
	}
}

func TestStopFlagDefaults(t *testing.T) {
	c := newStopCommand()
	cases := map[string]string{
		"pidfile":  "vortex.pid",
		"timeout":  (10 * time.Second).String(),
		"api-port": "9090",
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

func TestStopWhenNotRunning(t *testing.T) {
	c := newStopCommand()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	// Point at a pidfile that does not exist.
	missing := filepath.Join(t.TempDir(), "vortex.pid")
	c.SetArgs([]string{"--pidfile", missing})

	if err := c.Execute(); err != nil {
		t.Fatalf("stop with no pidfile should exit 0, got error: %v", err)
	}
	if !strings.Contains(buf.String(), "is not running") {
		t.Errorf("output should say 'is not running':\n%s", buf.String())
	}
}
