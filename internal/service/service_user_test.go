package service

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stubSeams replaces the user/PATH/command seams for one test and records
// every command that would have run.
func stubSeams(t *testing.T, exists bool, onPath ...string) *[]string {
	t.Helper()
	oldExists, oldLook, oldRun := userExists, lookPath, runCmd
	t.Cleanup(func() { userExists, lookPath, runCmd = oldExists, oldLook, oldRun })
	var ran []string
	userExists = func(string) bool { return exists }
	lookPath = func(name string) (string, error) {
		for _, p := range onPath {
			if p == name {
				return "/usr/sbin/" + name, nil
			}
		}
		return "", exec.ErrNotFound
	}
	runCmd = func(name string, args ...string) error {
		ran = append(ran, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	return &ran
}

func TestEnsureServiceUser_DryRunUseradd(t *testing.T) {
	ran := stubSeams(t, false, "useradd")
	var out bytes.Buffer
	if err := ensureServiceUser(InstallConfig{DryRun: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 0 {
		t.Fatalf("dry run executed commands: %v", *ran)
	}
	want := "useradd --system --user-group --home-dir /var/lib/vortex --create-home --shell /usr/sbin/nologin vortex"
	if !strings.Contains(out.String(), want) {
		t.Errorf("dry-run output %q missing %q", out.String(), want)
	}
}

func TestEnsureServiceUser_BusyBox(t *testing.T) {
	stubSeams(t, false, "adduser", "addgroup")
	var out bytes.Buffer
	if err := ensureServiceUser(InstallConfig{DryRun: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"addgroup -S vortex", "adduser -S -D -h /var/lib/vortex -s /sbin/nologin -G vortex vortex"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("BusyBox dry run missing %q:\n%s", want, out.String())
		}
	}
}

func TestEnsureServiceUser_ExistingUserIsNotRecreated(t *testing.T) {
	ran := stubSeams(t, true, "useradd")
	var out bytes.Buffer
	if err := ensureServiceUser(InstallConfig{DryRun: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "useradd") || len(*ran) != 0 {
		t.Errorf("existing user would be recreated: out=%q ran=%v", out.String(), *ran)
	}
}

func TestEnsureServiceUser_NoToolFailsForRealRuns(t *testing.T) {
	stubSeams(t, false)
	err := ensureServiceUser(InstallConfig{Out: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "create it manually") {
		t.Fatalf("err = %v, want a manual-creation error", err)
	}
	// A dry run only reports it.
	if err := ensureServiceUser(InstallConfig{DryRun: true, Out: &bytes.Buffer{}}); err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
}

func TestEnsureServiceUser_CommandFailureStops(t *testing.T) {
	stubSeams(t, false, "useradd")
	runCmd = func(string, ...string) error { return errors.New("useradd: permission denied") }
	if err := ensureServiceUser(InstallConfig{Out: &bytes.Buffer{}}); err == nil {
		t.Fatal("a failed useradd must abort the install")
	}
}

func TestInstallSystemdDryRun_CreatesUserBeforeUnit(t *testing.T) {
	stubSeams(t, false, "useradd")
	var out bytes.Buffer
	if err := Install(InstallConfig{ExecPath: "/usr/bin/vortex", ConfigPath: "/etc/vortex/vortex.cue",
		InitSystem: InitSystemd, DryRun: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	u, w := strings.Index(s, "useradd"), strings.Index(s, "would write systemd unit")
	if u < 0 || w < 0 || u > w {
		t.Fatalf("user creation should come before the unit:\n%s", s)
	}
}

func TestGenerateSystemd_Hardening(t *testing.T) {
	unit := GenerateSystemd(DefaultSystemdConfig("/usr/bin/vortex", "/etc/vortex/vortex.cue"))
	for _, want := range []string{
		"User=vortex", "WorkingDirectory=/var/lib/vortex",
		"AmbientCapabilities=CAP_NET_BIND_SERVICE", "CapabilityBoundingSet=CAP_NET_BIND_SERVICE",
		"NoNewPrivileges=true", "ProtectKernelTunables=true", "ProtectKernelModules=true",
		"ProtectKernelLogs=true", "ProtectControlGroups=true", "RestrictSUIDSGID=true",
		"SystemCallArchitectures=native",
	} {
		if !strings.Contains(unit, want+"\n") {
			t.Errorf("unit missing %q", want)
		}
	}
	// These would break the product (see the template comment).
	for _, banned := range []string{"MemoryDenyWriteExecute", "ProtectSystem", "ProtectHome",
		"RestrictNamespaces", "ProtectClock", "PrivateDevices", "DynamicUser"} {
		if strings.Contains(unit, banned) {
			t.Errorf("unit contains %s, which breaks VORTEX features", banned)
		}
	}
}

func TestGenerateOpenRC_BindCapabilityAndDirectory(t *testing.T) {
	s := GenerateOpenRC(DefaultOpenRCConfig("/usr/bin/vortex", "/etc/vortex/vortex.cue"))
	for _, want := range []string{"directory=/var/lib/vortex\n", `capabilities="^cap_net_bind_service"`} {
		if !strings.Contains(s, want) {
			t.Errorf("OpenRC script missing %q", want)
		}
	}
}

// On hosts with systemd-analyze (the Ubuntu CI runner), check that systemd
// itself accepts every directive in the generated unit.
func TestGenerateSystemd_SystemdAnalyzeVerify(t *testing.T) {
	sa, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze not available")
	}
	dir := t.TempDir()
	cfg := DefaultSystemdConfig("/bin/true", "/dev/null")
	cfg.User, cfg.Group = "root", "root" // the vortex user doesn't exist on the test host
	path := filepath.Join(dir, "vortex.service")
	if err := os.WriteFile(path, []byte(GenerateSystemd(cfg)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command(sa, "verify", path).CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		l := strings.ToLower(line)
		if strings.Contains(l, "unknown key") || strings.Contains(l, "unknown lvalue") ||
			strings.Contains(l, "unknown section") || strings.Contains(l, "failed to parse") {
			t.Errorf("systemd-analyze: %s", line)
		}
	}
}
