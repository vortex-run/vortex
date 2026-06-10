package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const secretTestConfig = `
cluster: {name: "secret-test-cluster"}
tls: {acme_email: "a@b.com"}
routes: []
security: {}
secrets: {keys: ["DB_PASSWORD", "JWT_SECRET"]}
observability: {}
`

func TestSecret_Registers(t *testing.T) {
	if newSecretCommand().Use != "secret" {
		t.Error("secret command Use should be 'secret'")
	}
}

func TestSecret_SetHasTwoArgs(t *testing.T) {
	c := newSecretSetCommand()
	if err := c.Args(c, []string{"only-one"}); err == nil {
		t.Error("set should require exactly two args")
	}
	if err := c.Args(c, []string{"name", "value"}); err != nil {
		t.Errorf("set with two args should be valid: %v", err)
	}
}

func TestSecret_GetHasRevealFlag(t *testing.T) {
	if newSecretGetCommand().Flags().Lookup("reveal") == nil {
		t.Error("get should have --reveal flag")
	}
}

func TestSecret_ListRegisters(t *testing.T) {
	if newSecretListCommand().Use != "list" {
		t.Error("list subcommand Use should be 'list'")
	}
}

func TestSecret_DeleteRegisters(t *testing.T) {
	if newSecretDeleteCommand().Use != "delete <name>" {
		t.Error("delete subcommand Use should be 'delete <name>'")
	}
}

func TestSecret_SetGetRoundTrip(t *testing.T) {
	// All three operations must share the same store + config, so run them in
	// one test with a fixed env (runSecret sets a fresh store each call, so we
	// drive the commands manually here against one store).
	storeDir := t.TempDir()
	t.Setenv("VORTEX_SECRET_STORE", storeDir)
	cfgPath := filepath.Join(t.TempDir(), "vortex.cue")
	if err := os.WriteFile(cfgPath, []byte(secretTestConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	old := flags.configPath
	flags.configPath = cfgPath
	t.Cleanup(func() { flags.configPath = old })

	run := func(args ...string) string {
		c := newSecretCommand()
		var buf bytes.Buffer
		c.SetOut(&buf)
		c.SetErr(&buf)
		c.SetArgs(args)
		if err := c.Execute(); err != nil {
			t.Fatalf("secret %v: %v\n%s", args, err, buf.String())
		}
		return buf.String()
	}

	if out := run("set", "TEST_KEY", "myvalue"); !strings.Contains(out, "set successfully") {
		t.Errorf("set output = %q", out)
	}
	if out := run("get", "TEST_KEY"); !strings.Contains(out, "[set]") {
		t.Errorf("get output = %q, want [set]", out)
	}
	if out := run("get", "TEST_KEY", "--reveal"); !strings.Contains(out, "myvalue") {
		t.Errorf("get --reveal output = %q, want myvalue", out)
	}
	if out := run("delete", "TEST_KEY"); !strings.Contains(out, "deleted") {
		t.Errorf("delete output = %q", out)
	}
	if out := run("get", "TEST_KEY"); !strings.Contains(out, "[not set]") {
		t.Errorf("get after delete = %q, want [not set]", out)
	}
}

func TestSecret_ListShowsDeclaredStatus(t *testing.T) {
	storeDir := t.TempDir()
	t.Setenv("VORTEX_SECRET_STORE", storeDir)
	cfgPath := filepath.Join(t.TempDir(), "vortex.cue")
	_ = os.WriteFile(cfgPath, []byte(secretTestConfig), 0o600)
	old := flags.configPath
	flags.configPath = cfgPath
	t.Cleanup(func() { flags.configPath = old })

	run := func(args ...string) string {
		c := newSecretCommand()
		var buf bytes.Buffer
		c.SetOut(&buf)
		c.SetErr(&buf)
		c.SetArgs(args)
		_ = c.Execute()
		return buf.String()
	}

	_ = run("set", "DB_PASSWORD", "x")
	out := run("list")
	if !strings.Contains(out, "Declared in config:") {
		t.Errorf("list missing declared header:\n%s", out)
	}
	if !strings.Contains(out, "DB_PASSWORD") || !strings.Contains(out, "[set]") {
		t.Errorf("list should show DB_PASSWORD [set]:\n%s", out)
	}
	if !strings.Contains(out, "JWT_SECRET") || !strings.Contains(out, "[not set]") {
		t.Errorf("list should show JWT_SECRET [not set]:\n%s", out)
	}
}

func TestSecretSetLifecycleFlags(t *testing.T) {
	c := newSecretSetCommand()
	for _, name := range []string{"expires-in", "rotate-every"} {
		if c.Flags().Lookup(name) == nil {
			t.Errorf("--%s flag not registered", name)
		}
	}
}

func TestParseLifetime(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"90d", 90 * 24 * time.Hour, false},
		{"1y", 365 * 24 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"36h", 36 * time.Hour, false},
		{"1.5d", 36 * time.Hour, false},
		{"", 0, true},
		{"abc", 0, true},
		{"-5d", 0, true},
		{"0d", 0, true},
	}
	for _, c := range cases {
		got, err := parseLifetime(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseLifetime(%q) should error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseLifetime(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseLifetime(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
