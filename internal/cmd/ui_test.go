package cmd

import (
	"context"
	"errors"
	"testing"
)

func TestUI_CommandRegisters(t *testing.T) {
	c := newUICommand()
	if c.Use != "ui" {
		t.Errorf("Use = %q, want ui", c.Use)
	}
	root := NewRootCommand()
	var found bool
	for _, sub := range root.Commands() {
		if sub.Name() == "ui" {
			found = true
		}
	}
	if !found {
		t.Error("ui should be registered on the root command")
	}
}

func TestUI_Flags(t *testing.T) {
	c := newUICommand()
	for _, name := range []string{"addr", "key", "start"} {
		if c.Flags().Lookup(name) == nil {
			t.Errorf("ui should have a --%s flag", name)
		}
	}
}

func TestUI_DisconnectedNoStartErrors(t *testing.T) {
	// Pointing at a dead address with start=false must return the sentinel
	// error promptly (not hang waiting for input or a server).
	err := runUI(context.Background(), "http://127.0.0.1:1", "", false)
	if !errors.Is(err, errUINotRunning) {
		t.Errorf("disconnected ui (no --start) err = %v, want errUINotRunning", err)
	}
}
