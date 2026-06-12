package cmd

import (
	"errors"
	"testing"
	"time"
)

func TestCode_CommandRegisters(t *testing.T) {
	c := newCodeCommand()
	if c.Use != "code" {
		t.Errorf("Use = %q, want code", c.Use)
	}
	root := NewRootCommand()
	var found bool
	for _, sub := range root.Commands() {
		if sub.Name() == "code" {
			found = true
		}
	}
	if !found {
		t.Error("code should be registered on the root command")
	}
}

func TestCode_Flags(t *testing.T) {
	c := newCodeCommand()
	for _, flag := range []string{"dir", "model", "no-team", "addr", "key"} {
		if c.Flags().Lookup(flag) == nil {
			t.Errorf("--%s flag missing", flag)
		}
	}
}

func TestCode_ExitsCleanlyWhenServerNotRunning(t *testing.T) {
	// Point at a dead port: runCode must return promptly with the sentinel
	// error, never hang or open the TUI.
	done := make(chan error, 1)
	go func() {
		done <- runCode("http://127.0.0.1:1", "k", "", "", false)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errCodeNotRunning) {
			t.Errorf("err = %v, want errCodeNotRunning", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("runCode hung with no server running")
	}
}
