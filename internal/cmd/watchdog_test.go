package cmd

import "testing"

func TestWatchdog_CommandRegisters(t *testing.T) {
	root := NewRootCommand()
	var wd bool
	for _, c := range root.Commands() {
		if c.Name() == "watchdog" {
			wd = true
		}
	}
	if !wd {
		t.Error("watchdog should be registered on the root command")
	}
}

func TestWatchdog_SubcommandsRegister(t *testing.T) {
	c := newWatchdogCommand()
	names := map[string]bool{}
	for _, sub := range c.Commands() {
		names[sub.Name()] = true
	}
	if !names["start"] || !names["status"] {
		t.Errorf("watchdog should have start + status subcommands, got %v", names)
	}
}

func TestWatchdog_StartFlags(t *testing.T) {
	c := newWatchdogStartCommand()
	for _, f := range []string{"pid-file", "binary", "config", "max-restarts", "notify"} {
		if c.Flags().Lookup(f) == nil {
			t.Errorf("watchdog start missing --%s flag", f)
		}
	}
}

func TestWatchdog_StatusDetectsProcess(t *testing.T) {
	// status against a missing pidfile should not error.
	c := newWatchdogStatusCommand()
	if err := c.Flags().Set("pid-file", "definitely-missing.pid"); err != nil {
		t.Fatal(err)
	}
	if err := c.RunE(c, nil); err != nil {
		t.Errorf("status should not error on a missing pidfile: %v", err)
	}
}
