package service

import (
	"bytes"
	"strings"
	"testing"
)

func TestFirewallAddArgs_AuthorisesTheBinaryNarrowly(t *testing.T) {
	args := firewallAddArgs(`C:\vortex\vortex.exe`, "")
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"advfirewall", "firewall", "add", "rule",
		"name=" + firewallRuleName,
		"dir=in", "action=allow",
		`program=C:\vortex\vortex.exe`,
		"enable=yes",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("rule is missing %q: %s", want, joined)
		}
	}

	// The default must NOT open the public profile. A VPS's public profile is
	// the internet-facing side; an installer that silently allowed it would
	// expose the management API far more widely than an operator expects.
	if !strings.Contains(joined, "profile=private,domain") {
		t.Errorf("default profiles = %q, want private,domain only", joined)
	}
	if strings.Contains(joined, "public") {
		t.Errorf("default rule must not include the public profile: %s", joined)
	}
}

func TestFirewallAddArgs_ProfilesOverride(t *testing.T) {
	args := firewallAddArgs(`C:\vortex\vortex.exe`, "private,domain,public")
	if !strings.Contains(strings.Join(args, " "), "profile=private,domain,public") {
		t.Errorf("explicit override not honoured: %v", args)
	}
}

// TestFirewallRule_AddAndDeleteUseTheSameName is what makes install idempotent
// and uninstall complete: a mismatch would leave stale rules accumulating on
// every reinstall.
func TestFirewallRule_AddAndDeleteUseTheSameName(t *testing.T) {
	add := strings.Join(firewallAddArgs("x", ""), " ")
	del := strings.Join(firewallDeleteArgs(), " ")
	if !strings.Contains(add, "name="+firewallRuleName) || !strings.Contains(del, "name="+firewallRuleName) {
		t.Errorf("add/delete disagree on the rule name:\n add: %s\n del: %s", add, del)
	}
}

func TestAddFirewallRule_DryRunExecutesNothingAndShowsBothCommands(t *testing.T) {
	var buf bytes.Buffer
	addFirewallRule(`C:\vortex\vortex.exe`, "", true, &buf)
	out := buf.String()
	if !strings.Contains(out, "[dry-run]") {
		t.Errorf("dry run did not announce itself: %s", out)
	}
	// Both the pre-delete and the add are shown, so an operator can reproduce
	// the exact commands by hand on a machine where netsh needs elevation.
	if strings.Count(out, "netsh") != 2 {
		t.Errorf("expected the delete and add commands, got: %s", out)
	}
	if !strings.Contains(out, "add rule") || !strings.Contains(out, "delete rule") {
		t.Errorf("dry run should show both commands: %s", out)
	}
}

func TestRemoveFirewallRule_DryRun(t *testing.T) {
	var buf bytes.Buffer
	removeFirewallRule(true, &buf)
	out := buf.String()
	if !strings.Contains(out, "[dry-run]") || !strings.Contains(out, "delete rule") {
		t.Errorf("unexpected dry-run output: %s", out)
	}
}

// TestInstallWindows_CreatesFirewallRule ties it to the installer: the point of
// the rule is that an unattended server start is never blocked behind an
// interactive dialog, so the Windows install path must actually emit it.
func TestInstallWindows_CreatesFirewallRule(t *testing.T) {
	var buf bytes.Buffer
	err := Install(InstallConfig{
		ExecPath:   `C:\vortex\vortex.exe`,
		ConfigPath: `C:\vortex\vortex.cue`,
		InitSystem: InitWindows,
		DryRun:     true,
		Out:        &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "nssm install vortex") {
		t.Errorf("NSSM guidance disappeared: %s", out)
	}
	if !strings.Contains(out, "advfirewall") {
		t.Errorf("Windows install did not emit a firewall rule: %s", out)
	}
}

func TestInstallWindows_SkipFirewallOptOut(t *testing.T) {
	var buf bytes.Buffer
	if err := Install(InstallConfig{
		ExecPath: `C:\vortex\vortex.exe`, ConfigPath: `C:\vortex\vortex.cue`,
		InitSystem: InitWindows, DryRun: true, Out: &buf, SkipFirewall: true,
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "advfirewall") {
		t.Errorf("--skip-firewall still emitted a rule: %s", buf.String())
	}
}

func TestUninstallWindows_RemovesFirewallRule(t *testing.T) {
	var buf bytes.Buffer
	if err := Uninstall(InstallConfig{InitSystem: InitWindows, DryRun: true, Out: &buf}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "delete rule") {
		t.Errorf("uninstall left the firewall rule behind: %s", buf.String())
	}
}

// TestInstallLinux_TouchesNoFirewall guards the blast radius: firewall
// management is Windows-only and must not leak into the Linux paths.
func TestInstallLinux_TouchesNoFirewall(t *testing.T) {
	for _, sys := range []InitSystem{InitSystemd, InitOpenRC} {
		var buf bytes.Buffer
		if err := Install(InstallConfig{
			ExecPath: "/usr/bin/vortex", ConfigPath: "/etc/vortex/vortex.cue",
			InitSystem: sys, DryRun: true, Out: &buf,
		}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(buf.String(), "advfirewall") || strings.Contains(buf.String(), "netsh") {
			t.Errorf("%s install emitted Windows firewall commands: %s", sys, buf.String())
		}
	}
}
