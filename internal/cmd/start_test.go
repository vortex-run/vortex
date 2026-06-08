package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStartCommandRegisters(t *testing.T) {
	c := newStartCommand()
	if c.Use != "start" {
		t.Errorf("Use = %q, want start", c.Use)
	}
}

func TestStartPidfileFlagDefault(t *testing.T) {
	c := newStartCommand()
	f := c.Flags().Lookup("pidfile")
	if f == nil {
		t.Fatal("--pidfile flag not registered")
	}
	if f.DefValue != "vortex.pid" {
		t.Errorf("--pidfile default = %q, want vortex.pid", f.DefValue)
	}
}

func TestRunStartReturnsCleanlyOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "vortex.cue")
	if err := os.WriteFile(cfgPath, []byte(checkValidConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(dir, "vortex.pid")

	// Point the global config flag at our temp config and initialise the logger
	// (normally done by PersistentPreRunE).
	oldCfg := flags.configPath
	flags.configPath = cfgPath
	t.Cleanup(func() { flags.configPath = oldCfg })
	ensureTestLogger()

	// A pre-cancelled context makes lifecycle.Run return immediately, so
	// runStart performs its full start→shutdown sequence without blocking.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := runStart(ctx, pidPath); err != nil {
		t.Fatalf("runStart returned error on clean shutdown: %v", err)
	}

	// PID file should have been removed by the shutdown hook.
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("pidfile should be removed after shutdown, stat err = %v", err)
	}
}

func TestResolveWorkingDir_IsProcessCwd(t *testing.T) {
	// Working dir is informational only: always the process cwd, no override.
	cwd, _ := os.Getwd()
	if got := resolveWorkingDir(); got != cwd && got != "." {
		t.Errorf("resolveWorkingDir = %q, want cwd %q", got, cwd)
	}
}

func TestStartCommand_NoCwdOrAllowPathFlag(t *testing.T) {
	// The path-restriction flags were removed — the approval gate is the only
	// control over where the agent writes.
	c := newStartCommand()
	if c.Flags().Lookup("cwd") != nil {
		t.Error("--cwd flag should have been removed")
	}
	if c.Flags().Lookup("allow-path") != nil {
		t.Error("--allow-path flag should not exist")
	}
}
