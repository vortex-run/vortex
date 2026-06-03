package service

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// InitSystem identifies the host's service manager.
type InitSystem string

const (
	InitSystemd InitSystem = "systemd"
	InitOpenRC  InitSystem = "openrc"
	InitUnknown InitSystem = "unknown"
	InitWindows InitSystem = "windows"
)

// File-system paths used for detection and installation. Declared as vars so
// tests could override them if needed.
const (
	systemdUnitPath = "/etc/systemd/system/vortex.service"
	openrcInitPath  = "/etc/init.d/vortex"
)

// DetectInitSystem inspects the running OS and returns its init system.
func DetectInitSystem() InitSystem {
	if runtime.GOOS == "windows" {
		return InitWindows
	}
	// systemd exposes a private control socket and PID 1 is "systemd".
	if _, err := os.Stat("/run/systemd/private"); err == nil {
		return InitSystemd
	}
	if comm, err := os.ReadFile("/proc/1/comm"); err == nil {
		if strings.TrimSpace(string(comm)) == "systemd" {
			return InitSystemd
		}
	}
	if _, err := os.Stat("/sbin/openrc-run"); err == nil {
		return InitOpenRC
	}
	return InitUnknown
}

// InstallConfig configures Install/Uninstall.
type InstallConfig struct {
	ExecPath   string
	ConfigPath string
	InitSystem InitSystem // if empty, auto-detect
	DryRun     bool       // print actions without executing
	Out        io.Writer  // where messages are written; defaults to os.Stdout
}

func (c *InstallConfig) out() io.Writer {
	if c.Out != nil {
		return c.Out
	}
	return os.Stdout
}

func (c *InstallConfig) initSystem() InitSystem {
	if c.InitSystem != "" {
		return c.InitSystem
	}
	return DetectInitSystem()
}

// Install installs VORTEX as a managed system service for the detected (or
// forced) init system. With DryRun, it prints the actions it would take and
// writes nothing.
func Install(cfg InstallConfig) error {
	switch cfg.initSystem() {
	case InitSystemd:
		return installSystemd(cfg)
	case InitOpenRC:
		return installOpenRC(cfg)
	case InitWindows:
		return installWindows(cfg)
	default:
		return installUnknown(cfg)
	}
}

func installSystemd(cfg InstallConfig) error {
	unit := GenerateSystemd(DefaultSystemdConfig(cfg.ExecPath, cfg.ConfigPath))
	out := cfg.out()
	if cfg.DryRun {
		fmt.Fprintf(out, "[dry-run] would write systemd unit to %s\n", systemdUnitPath)
		fmt.Fprintln(out, "[dry-run] would run: systemctl daemon-reload")
		fmt.Fprintln(out, "[dry-run] would run: systemctl enable vortex")
		return nil
	}
	if err := os.WriteFile(systemdUnitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("writing systemd unit: %w", err)
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "vortex"); err != nil {
		return err
	}
	fmt.Fprintln(out, "Installed systemd service. Start with: systemctl start vortex")
	return nil
}

func installOpenRC(cfg InstallConfig) error {
	script := GenerateOpenRC(DefaultOpenRCConfig(cfg.ExecPath, cfg.ConfigPath))
	out := cfg.out()
	if cfg.DryRun {
		fmt.Fprintf(out, "[dry-run] would write OpenRC script to %s (mode 0755)\n", openrcInitPath)
		fmt.Fprintln(out, "[dry-run] would run: rc-update add vortex default")
		return nil
	}
	if err := os.WriteFile(openrcInitPath, []byte(script), 0o755); err != nil {
		return fmt.Errorf("writing OpenRC script: %w", err)
	}
	if err := run("rc-update", "add", "vortex", "default"); err != nil {
		return err
	}
	fmt.Fprintln(out, "Installed OpenRC service. Start with: rc-service vortex start")
	return nil
}

func installWindows(cfg InstallConfig) error {
	out := cfg.out()
	fmt.Fprintln(out, "Windows has no built-in service installer for VORTEX.")
	fmt.Fprintln(out, "Run VORTEX as a Windows Service via NSSM.")
	fmt.Fprintln(out, "Download NSSM from nssm.cc then run:")
	fmt.Fprintf(out, "  nssm install vortex %s start --config %s\n", cfg.ExecPath, cfg.ConfigPath)
	return nil
}

func installUnknown(cfg InstallConfig) error {
	out := cfg.out()
	unit := GenerateSystemd(DefaultSystemdConfig(cfg.ExecPath, cfg.ConfigPath))
	script := GenerateOpenRC(DefaultOpenRCConfig(cfg.ExecPath, cfg.ConfigPath))
	if cfg.DryRun {
		fmt.Fprintln(out, "[dry-run] could not detect init system")
		fmt.Fprintln(out, "[dry-run] would write ./vortex.service and ./vortex-openrc")
		return nil
	}
	if err := os.WriteFile("vortex.service", []byte(unit), 0o644); err != nil {
		return fmt.Errorf("writing vortex.service: %w", err)
	}
	if err := os.WriteFile("vortex-openrc", []byte(script), 0o755); err != nil {
		return fmt.Errorf("writing vortex-openrc: %w", err)
	}
	fmt.Fprintln(out, "Could not detect init system. Files written to current directory.")
	return nil
}

// Uninstall reverses Install for the given (or detected) init system.
func Uninstall(cfg InstallConfig) error {
	initSys := cfg.initSystem()
	out := cfg.out()
	switch initSys {
	case InitSystemd:
		if cfg.DryRun {
			fmt.Fprintln(out, "[dry-run] would run: systemctl stop vortex")
			fmt.Fprintln(out, "[dry-run] would run: systemctl disable vortex")
			fmt.Fprintf(out, "[dry-run] would remove %s\n", systemdUnitPath)
			fmt.Fprintln(out, "[dry-run] would run: systemctl daemon-reload")
			return nil
		}
		_ = run("systemctl", "stop", "vortex")
		_ = run("systemctl", "disable", "vortex")
		if err := os.Remove(systemdUnitPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing systemd unit: %w", err)
		}
		_ = run("systemctl", "daemon-reload")
		fmt.Fprintln(out, "Removed systemd service.")
	case InitOpenRC:
		if cfg.DryRun {
			fmt.Fprintln(out, "[dry-run] would run: rc-service vortex stop")
			fmt.Fprintln(out, "[dry-run] would run: rc-update del vortex default")
			fmt.Fprintf(out, "[dry-run] would remove %s\n", openrcInitPath)
			return nil
		}
		_ = run("rc-service", "vortex", "stop")
		_ = run("rc-update", "del", "vortex", "default")
		if err := os.Remove(openrcInitPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing OpenRC script: %w", err)
		}
		fmt.Fprintln(out, "Removed OpenRC service.")
	case InitWindows:
		fmt.Fprintln(out, "Remove the Windows Service with: nssm remove vortex confirm")
	default:
		if cfg.DryRun {
			fmt.Fprintln(out, "[dry-run] would remove ./vortex.service and ./vortex-openrc")
			return nil
		}
		_ = os.Remove(filepath.Clean("vortex.service"))
		_ = os.Remove(filepath.Clean("vortex-openrc"))
		fmt.Fprintln(out, "Removed generated service files from current directory.")
	}
	return nil
}

// run executes a command, attaching its stdout/stderr to the process, and
// wraps any failure with context.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
