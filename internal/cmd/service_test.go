package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestServiceCommandRegisters(t *testing.T) {
	if newServiceCommand().Use != "service" {
		t.Error("service command Use should be 'service'")
	}
}

func TestServiceInstallHasDryRunFlag(t *testing.T) {
	c := newServiceInstallCommand()
	if c.Flags().Lookup("dry-run") == nil {
		t.Error("install should have --dry-run flag")
	}
}

func TestServiceGenerateHasFormatFlag(t *testing.T) {
	c := newServiceGenerateCommand()
	if c.Flags().Lookup("format") == nil {
		t.Error("generate should have --format flag")
	}
}

// runGenerate executes the generate subcommand with the given args, capturing
// combined output.
func runGenerate(t *testing.T, args ...string) string {
	t.Helper()
	c := newServiceGenerateCommand()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	// Provide explicit paths so output does not depend on global flag state.
	c.SetArgs(append([]string{
		"--exec-path", "/usr/local/bin/vortex",
		"--config-path", "/etc/vortex/vortex.cue",
	}, args...))
	if err := c.Execute(); err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	return buf.String()
}

func TestServiceGenerateSystemd(t *testing.T) {
	out := runGenerate(t, "--format", "systemd")
	if !strings.Contains(out, "[Unit]") {
		t.Errorf("systemd output should contain [Unit]:\n%s", out)
	}
}

func TestServiceGenerateOpenRC(t *testing.T) {
	out := runGenerate(t, "--format", "openrc")
	if !strings.HasPrefix(out, "#!/sbin/openrc-run") {
		t.Errorf("openrc output should start with #!/sbin/openrc-run:\n%s", out)
	}
}
