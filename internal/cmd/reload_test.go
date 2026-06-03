package cmd

import "testing"

func TestReloadCommandRegisters(t *testing.T) {
	if newReloadCommand().Use != "reload" {
		t.Error("reload command Use should be 'reload'")
	}
}

func TestReloadFlagDefaults(t *testing.T) {
	c := newReloadCommand()
	cases := map[string]string{
		"pidfile":  "vortex.pid",
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
