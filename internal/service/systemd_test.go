package service

import (
	"strings"
	"testing"
)

func TestGenerateSystemdHasSections(t *testing.T) {
	out := GenerateSystemd(DefaultSystemdConfig("/usr/local/bin/vortex", "/etc/vortex/vortex.cue"))
	for _, section := range []string{"[Unit]", "[Service]", "[Install]"} {
		if !strings.Contains(out, section) {
			t.Errorf("unit file missing section %q\n%s", section, out)
		}
	}
}

func TestGenerateSystemdExecStart(t *testing.T) {
	out := GenerateSystemd(DefaultSystemdConfig("/opt/vortex/vortex", "/etc/vortex/vortex.cue"))
	if !strings.Contains(out, "ExecStart=/opt/vortex/vortex start --config /etc/vortex/vortex.cue") {
		t.Errorf("ExecStart line incorrect:\n%s", out)
	}
}

func TestGenerateSystemdExecReloadHUP(t *testing.T) {
	out := GenerateSystemd(DefaultSystemdConfig("/usr/local/bin/vortex", "/etc/vortex/vortex.cue"))
	if !strings.Contains(out, "ExecReload=/bin/kill -HUP $MAINPID") {
		t.Errorf("ExecReload should send -HUP:\n%s", out)
	}
}

func TestGenerateSystemdLimitNOFILE(t *testing.T) {
	out := GenerateSystemd(DefaultSystemdConfig("/usr/local/bin/vortex", "/etc/vortex/vortex.cue"))
	if !strings.Contains(out, "LimitNOFILE=65536") {
		t.Errorf("LimitNOFILE=65536 missing:\n%s", out)
	}
}

func TestGenerateSystemdUserGroup(t *testing.T) {
	cfg := DefaultSystemdConfig("/usr/local/bin/vortex", "/etc/vortex/vortex.cue")
	cfg.User = "svc-user"
	cfg.Group = "svc-group"
	out := GenerateSystemd(cfg)
	if !strings.Contains(out, "User=svc-user") {
		t.Errorf("User not reflected:\n%s", out)
	}
	if !strings.Contains(out, "Group=svc-group") {
		t.Errorf("Group not reflected:\n%s", out)
	}
}

func TestGenerateSystemdCustomDescription(t *testing.T) {
	cfg := DefaultSystemdConfig("/usr/local/bin/vortex", "/etc/vortex/vortex.cue")
	cfg.Description = "My Custom VORTEX"
	out := GenerateSystemd(cfg)
	if !strings.Contains(out, "Description=My Custom VORTEX") {
		t.Errorf("custom description not reflected:\n%s", out)
	}
}

func TestDefaultSystemdConfig(t *testing.T) {
	cfg := DefaultSystemdConfig("/usr/local/bin/vortex", "/etc/vortex/vortex.cue")
	if cfg.ExecPath != "/usr/local/bin/vortex" {
		t.Errorf("ExecPath = %q", cfg.ExecPath)
	}
	if cfg.ConfigPath != "/etc/vortex/vortex.cue" {
		t.Errorf("ConfigPath = %q", cfg.ConfigPath)
	}
	if cfg.User != "vortex" {
		t.Errorf("User default = %q, want vortex", cfg.User)
	}
	if cfg.Group != "vortex" {
		t.Errorf("Group default = %q, want vortex", cfg.Group)
	}
	if cfg.Description != "VORTEX — autonomous infra platform" {
		t.Errorf("Description default = %q", cfg.Description)
	}
	if cfg.After != "network-online.target" {
		t.Errorf("After default = %q, want network-online.target", cfg.After)
	}
}
