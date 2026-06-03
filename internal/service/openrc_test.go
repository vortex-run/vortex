package service

import (
	"strings"
	"testing"
)

func TestGenerateOpenRCShebang(t *testing.T) {
	out := GenerateOpenRC(DefaultOpenRCConfig("/usr/local/bin/vortex", "/etc/vortex/vortex.cue"))
	if !strings.HasPrefix(out, "#!/sbin/openrc-run") {
		t.Errorf("script should start with #!/sbin/openrc-run:\n%s", out)
	}
}

func TestGenerateOpenRCCommand(t *testing.T) {
	out := GenerateOpenRC(DefaultOpenRCConfig("/opt/vortex/vortex", "/etc/vortex/vortex.cue"))
	if !strings.Contains(out, "command=/opt/vortex/vortex") {
		t.Errorf("command= path incorrect:\n%s", out)
	}
}

func TestGenerateOpenRCCommandArgsConfig(t *testing.T) {
	out := GenerateOpenRC(DefaultOpenRCConfig("/usr/local/bin/vortex", "/etc/vortex/vortex.cue"))
	if !strings.Contains(out, "--config /etc/vortex/vortex.cue") {
		t.Errorf("command_args should contain --config:\n%s", out)
	}
}

func TestGenerateOpenRCPidfile(t *testing.T) {
	out := GenerateOpenRC(DefaultOpenRCConfig("/usr/local/bin/vortex", "/etc/vortex/vortex.cue"))
	if !strings.Contains(out, "pidfile=/run/vortex/vortex.pid") {
		t.Errorf("pidfile not set:\n%s", out)
	}
}

func TestGenerateOpenRCDependNeedNet(t *testing.T) {
	out := GenerateOpenRC(DefaultOpenRCConfig("/usr/local/bin/vortex", "/etc/vortex/vortex.cue"))
	if !strings.Contains(out, "need net") {
		t.Errorf("depend block should contain 'need net':\n%s", out)
	}
}

func TestDefaultOpenRCConfig(t *testing.T) {
	cfg := DefaultOpenRCConfig("/usr/local/bin/vortex", "/etc/vortex/vortex.cue")
	if cfg.ExecPath != "/usr/local/bin/vortex" {
		t.Errorf("ExecPath = %q", cfg.ExecPath)
	}
	if cfg.ConfigPath != "/etc/vortex/vortex.cue" {
		t.Errorf("ConfigPath = %q", cfg.ConfigPath)
	}
	if cfg.User != "vortex" {
		t.Errorf("User default = %q, want vortex", cfg.User)
	}
	if cfg.PidFile != "/run/vortex/vortex.pid" {
		t.Errorf("PidFile default = %q", cfg.PidFile)
	}
	if cfg.LogFile != "/var/log/vortex/vortex.log" {
		t.Errorf("LogFile default = %q", cfg.LogFile)
	}
}
