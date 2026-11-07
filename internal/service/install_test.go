package service

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetectInitSystemOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific detection check")
	}
	if got := DetectInitSystem(); got != InitWindows {
		t.Errorf("DetectInitSystem() = %q, want windows", got)
	}
}

func TestInstallDryRunWritesNoFiles(t *testing.T) {
	// Run in an empty temp dir so the unknown-fallback would be visible if it
	// wrote anything.
	dir := t.TempDir()
	t.Chdir(dir)

	var buf bytes.Buffer
	cfg := InstallConfig{
		ExecPath:   "/usr/local/bin/vortex",
		ConfigPath: "/etc/vortex/gubern.cue",
		InitSystem: InitSystemd,
		DryRun:     true,
		Out:        &buf,
	}
	if err := Install(cfg); err != nil {
		t.Fatalf("dry-run Install: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dry-run should write no files, found: %v", names)
	}
	// Also confirm the systemd unit path itself was not created.
	if _, err := os.Stat(currentNames.Unit); err == nil {
		t.Errorf("dry-run should not create %s", currentNames.Unit)
	}
}

func TestInstallDryRunPrintsSystemdActions(t *testing.T) {
	var buf bytes.Buffer
	cfg := InstallConfig{
		ExecPath:   "/usr/local/bin/vortex",
		ConfigPath: "/etc/vortex/gubern.cue",
		InitSystem: InitSystemd,
		DryRun:     true,
		Out:        &buf,
	}
	if err := Install(cfg); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"daemon-reload", "systemctl enable gubern", currentNames.Unit} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, out)
		}
	}
}

func TestInstallDryRunWindowsPrintsNSSM(t *testing.T) {
	var buf bytes.Buffer
	cfg := InstallConfig{
		ExecPath:   `C:\Program Files\vortex\vortex.exe`,
		ConfigPath: `C:\ProgramData\vortex\gubern.cue`,
		InitSystem: InitWindows,
		DryRun:     true,
		Out:        &buf,
	}
	if err := Install(cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "nssm install gubern") {
		t.Errorf("Windows install should mention NSSM:\n%s", buf.String())
	}
}

func TestUninstallDryRunPrintsActions(t *testing.T) {
	var buf bytes.Buffer
	cfg := InstallConfig{
		InitSystem: InitSystemd,
		DryRun:     true,
		Out:        &buf,
	}
	if err := Uninstall(cfg); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"systemctl stop gubern", "systemctl disable gubern"} {
		if !strings.Contains(out, want) {
			t.Errorf("uninstall dry-run missing %q:\n%s", want, out)
		}
	}
}

func TestInstallUnknownDryRunNoFiles(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var buf bytes.Buffer
	cfg := InstallConfig{
		ExecPath:   "/usr/local/bin/vortex",
		ConfigPath: "/etc/vortex/gubern.cue",
		InitSystem: InitUnknown,
		DryRun:     true,
		Out:        &buf,
	}
	err := Install(cfg)
	if runtime.GOOS == "darwin" {
		// macOS has no launchd support yet and says so instead of writing
		// Linux unit files nobody can use.
		if !errors.Is(err, errDarwinUnsupported) {
			t.Fatalf("err = %v, want errDarwinUnsupported", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"gubern.service", "vortex.service"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("unknown dry-run should not write %s", f)
		}
	}
}

// TestInstallUnknown_DarwinRefuses: on macOS, install must not drop Linux
// systemd/OpenRC files into the working directory.
func TestInstallUnknown_DarwinRefuses(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	err := Install(InstallConfig{ExecPath: "/usr/local/bin/vortex", ConfigPath: "/etc/vortex/gubern.cue", InitSystem: InitUnknown})
	if !errors.Is(err, errDarwinUnsupported) {
		t.Fatalf("Install on darwin = %v, want errDarwinUnsupported", err)
	}
	for _, f := range []string{"gubern.service", "gubern-openrc", "vortex.service", "vortex-openrc"} {
		if _, serr := os.Stat(filepath.Join(dir, f)); serr == nil {
			t.Errorf("wrote %s on macOS", f)
		}
	}
}

// TestDetectNames_LegacyInstallKeepsItsNames: a host that already runs the
// pre-rename vortex service (unit file or account present) keeps the vortex
// unit, account and /var/lib/vortex home, so reinstalling migrates nothing.
func TestDetectNames_LegacyInstallKeepsItsNames(t *testing.T) {
	oldExists, oldPath := userExists, pathExists
	t.Cleanup(func() { userExists, pathExists = oldExists, oldPath })

	userExists = func(string) bool { return false }
	pathExists = func(string) bool { return false }
	if n := detectNames(); n != currentNames {
		t.Errorf("fresh host: %+v", n)
	}
	pathExists = func(p string) bool { return p == "/etc/systemd/system/vortex.service" }
	if n := detectNames(); n != legacyNames {
		t.Errorf("legacy unit present: %+v", n)
	}
	pathExists = func(string) bool { return false }
	userExists = func(u string) bool { return u == "vortex" }
	if n := detectNames(); n != legacyNames {
		t.Errorf("legacy account present: %+v", n)
	}

	// The legacy unit keeps User/WorkingDirectory and is enabled by its name.
	var buf bytes.Buffer
	stubSeams(t, true, "useradd")
	if err := Install(InstallConfig{ExecPath: "/usr/local/bin/gubern", ConfigPath: "/etc/vortex/vortex.cue",
		InitSystem: InitSystemd, DryRun: true, Out: &buf, Names: &legacyNames}); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "/etc/systemd/system/vortex.service") || !strings.Contains(out, "systemctl enable vortex") {
		t.Errorf("legacy install dry run:\n%s", out)
	}
	unit := GenerateSystemd(systemdConfigFor(legacyNames, "/usr/local/bin/gubern", "/etc/vortex/vortex.cue"))
	for _, want := range []string{"User=vortex", "WorkingDirectory=/var/lib/vortex", "ExecStart=/usr/local/bin/gubern"} {
		if !strings.Contains(unit, want) {
			t.Errorf("legacy unit lacks %q:\n%s", want, unit)
		}
	}
}
