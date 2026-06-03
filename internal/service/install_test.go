package service

import (
	"bytes"
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
		ConfigPath: "/etc/vortex/vortex.cue",
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
	if _, err := os.Stat(systemdUnitPath); err == nil {
		t.Errorf("dry-run should not create %s", systemdUnitPath)
	}
}

func TestInstallDryRunPrintsSystemdActions(t *testing.T) {
	var buf bytes.Buffer
	cfg := InstallConfig{
		ExecPath:   "/usr/local/bin/vortex",
		ConfigPath: "/etc/vortex/vortex.cue",
		InitSystem: InitSystemd,
		DryRun:     true,
		Out:        &buf,
	}
	if err := Install(cfg); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"daemon-reload", "systemctl enable vortex", systemdUnitPath} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, out)
		}
	}
}

func TestInstallDryRunWindowsPrintsNSSM(t *testing.T) {
	var buf bytes.Buffer
	cfg := InstallConfig{
		ExecPath:   `C:\Program Files\vortex\vortex.exe`,
		ConfigPath: `C:\ProgramData\vortex\vortex.cue`,
		InitSystem: InitWindows,
		DryRun:     true,
		Out:        &buf,
	}
	if err := Install(cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "nssm install vortex") {
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
	for _, want := range []string{"systemctl stop vortex", "systemctl disable vortex"} {
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
		ConfigPath: "/etc/vortex/vortex.cue",
		InitSystem: InitUnknown,
		DryRun:     true,
		Out:        &buf,
	}
	if err := Install(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vortex.service")); err == nil {
		t.Error("unknown dry-run should not write vortex.service")
	}
}
