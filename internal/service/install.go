package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
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

// ServiceUser and ServiceHome are the unprivileged account the Linux service
// runs as and its home (also the working directory, so the agent's default
// workspace is not /), for new installs.
const (
	ServiceUser = "gubern"
	ServiceHome = "/var/lib/gubern"
)

// serviceNames are the names one installed service uses: the service/unit
// name, its account, home, and unit/init-script paths.
type serviceNames struct {
	Name, User, Home, Unit, InitScript string
}

// currentNames are used on hosts without a pre-rename install.
var currentNames = serviceNames{
	Name: "gubern", User: ServiceUser, Home: ServiceHome,
	Unit: "/etc/systemd/system/gubern.service", InitScript: "/etc/init.d/gubern",
}

// legacyNames are the pre-rename (VORTEX) names. A host that already has the
// vortex unit, init script or account keeps using them, so its service, its
// account and the keys and data in /var/lib/vortex stay where they are:
// reinstalling only points the unit at the new binary. Nothing is migrated.
var legacyNames = serviceNames{
	Name: "vortex", User: "vortex", Home: "/var/lib/vortex",
	Unit: "/etc/systemd/system/vortex.service", InitScript: "/etc/init.d/vortex",
}

// pathExists is a seam for tests.
var pathExists = func(p string) bool { _, err := os.Stat(p); return err == nil }

// detectNames picks legacyNames when a pre-rename service or account exists.
func detectNames() serviceNames {
	if pathExists(legacyNames.Unit) || pathExists(legacyNames.InitScript) || userExists(legacyNames.User) {
		return legacyNames
	}
	return currentNames
}

// Seams for tests: user lookup, PATH lookup, and command execution.
var (
	userExists = func(name string) bool { _, err := user.Lookup(name); return err == nil }
	lookPath   = exec.LookPath
	runCmd     = run
)

// ensureServiceUser creates the system account the unit runs as. Without it
// systemd refuses to start the service (status 217/USER) and OpenRC's
// start-stop-daemon fails. An existing account is left as it is, apart from
// making sure its home exists and belongs to it.
func ensureServiceUser(cfg InstallConfig) error {
	out := cfg.out()
	n := cfg.names()
	ServiceUser, ServiceHome := n.User, n.Home
	if userExists(ServiceUser) {
		if cfg.DryRun {
			fmt.Fprintf(out, "[dry-run] user %s exists; would ensure %s exists and is owned by it\n", ServiceUser, ServiceHome)
			return nil
		}
		return ensureServiceHome(n)
	}
	var cmds [][]string
	switch {
	case lookPathOK("useradd"):
		cmds = [][]string{{"useradd", "--system", "--user-group", "--home-dir", ServiceHome,
			"--create-home", "--shell", "/usr/sbin/nologin", ServiceUser}}
	case lookPathOK("adduser") && lookPathOK("addgroup"): // BusyBox (Alpine)
		cmds = [][]string{
			{"addgroup", "-S", ServiceUser},
			{"adduser", "-S", "-D", "-h", ServiceHome, "-s", "/sbin/nologin", "-G", ServiceUser, ServiceUser},
		}
	default:
		if cfg.DryRun {
			fmt.Fprintf(out, "[dry-run] would create system user %s (home %s), but neither useradd nor adduser is on PATH here\n", ServiceUser, ServiceHome)
			return nil
		}
		return fmt.Errorf("creating service user %q: neither useradd nor adduser is available; create it manually with home %s", ServiceUser, ServiceHome)
	}
	for _, c := range cmds {
		if cfg.DryRun {
			fmt.Fprintf(out, "[dry-run] would run: %s\n", strings.Join(c, " "))
			continue
		}
		if err := runCmd(c[0], c[1:]...); err != nil {
			return err
		}
	}
	if cfg.DryRun {
		return nil
	}
	return ensureServiceHome(n)
}

// ensureServiceHome creates the service home and hands it to the service user.
func ensureServiceHome(n serviceNames) error {
	if err := os.MkdirAll(n.Home, 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", n.Home, err)
	}
	return runCmd("chown", n.User+":", n.Home)
}

func lookPathOK(name string) bool { _, err := lookPath(name); return err == nil }

// errDarwinUnsupported is returned by Install on macOS, which has no launchd
// support yet.
var errDarwinUnsupported = errors.New("gubern service install doesn't support macOS (launchd) yet; " +
	"run `gubern start --config <path>` in a terminal, or under your own launchd job")

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
	// SkipFirewall disables Windows Firewall rule management. No effect on
	// Linux, where the installer never touches the firewall.
	SkipFirewall bool
	// FirewallProfiles overrides which netsh profiles the inbound rule covers
	// (default "private,domain" — public is excluded deliberately). Set to
	// "private,domain,public" to expose Gubern on untrusted networks.
	FirewallProfiles string
	// Names overrides the detected service names (tests).
	Names *serviceNames
}

// names returns the service names to use: Names if set, otherwise the
// pre-rename ones when such a service or account exists (detectNames).
func (c *InstallConfig) names() serviceNames {
	if c.Names != nil {
		return *c.Names
	}
	return detectNames()
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

// Install installs Gubern as a managed system service for the detected (or
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
	n := cfg.names()
	unit := GenerateSystemd(systemdConfigFor(n, cfg.ExecPath, cfg.ConfigPath))
	out := cfg.out()
	if err := ensureServiceUser(cfg); err != nil {
		return err
	}
	if cfg.DryRun {
		fmt.Fprintf(out, "[dry-run] would write systemd unit to %s\n", n.Unit)
		fmt.Fprintln(out, "[dry-run] would run: systemctl daemon-reload")
		fmt.Fprintf(out, "[dry-run] would run: systemctl enable %s\n", n.Name)
		return nil
	}
	if err := os.WriteFile(n.Unit, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("writing systemd unit: %w", err)
	}
	if err := runCmd("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := runCmd("systemctl", "enable", n.Name); err != nil {
		return err
	}
	fmt.Fprintf(out, "Installed systemd service. Start with: systemctl start %s\n", n.Name)
	return nil
}

// systemdConfigFor renders the unit for the chosen names (a pre-rename host
// keeps its vortex account and home).
func systemdConfigFor(n serviceNames, execPath, configPath string) SystemdConfig {
	c := DefaultSystemdConfig(execPath, configPath)
	c.User, c.Group, c.Home = n.User, n.User, n.Home
	return c
}

func openrcConfigFor(n serviceNames, execPath, configPath string) OpenRCConfig {
	c := DefaultOpenRCConfig(execPath, configPath)
	c.User, c.Home = n.User, n.Home
	c.PidFile = "/run/" + n.Name + "/" + n.Name + ".pid"
	return c
}

func installOpenRC(cfg InstallConfig) error {
	n := cfg.names()
	script := GenerateOpenRC(openrcConfigFor(n, cfg.ExecPath, cfg.ConfigPath))
	out := cfg.out()
	if err := ensureServiceUser(cfg); err != nil {
		return err
	}
	if cfg.DryRun {
		fmt.Fprintf(out, "[dry-run] would write OpenRC script to %s (mode 0755)\n", n.InitScript)
		fmt.Fprintf(out, "[dry-run] would run: rc-update add %s default\n", n.Name)
		return nil
	}
	if err := os.WriteFile(n.InitScript, []byte(script), 0o755); err != nil {
		return fmt.Errorf("writing OpenRC script: %w", err)
	}
	if err := runCmd("rc-update", "add", n.Name, "default"); err != nil {
		return err
	}
	fmt.Fprintf(out, "Installed OpenRC service. Start with: rc-service %s start\n", n.Name)
	return nil
}

func installWindows(cfg InstallConfig) error {
	out := cfg.out()
	fmt.Fprintln(out, "Windows has no built-in service installer for Gubern.")
	fmt.Fprintln(out, "Run Gubern as a Windows Service via NSSM.")
	fmt.Fprintln(out, "Download NSSM from nssm.cc then run:")
	fmt.Fprintf(out, "  nssm install gubern %s start --config %s\n", cfg.ExecPath, cfg.ConfigPath)

	// Pre-authorise the binary so the first start does not sit behind an
	// interactive firewall dialog that nobody is present to answer.
	if !cfg.SkipFirewall {
		addFirewallRule(cfg.ExecPath, cfg.FirewallProfiles, cfg.DryRun, out)
	}
	return nil
}

func installUnknown(cfg InstallConfig) error {
	out := cfg.out()
	if runtime.GOOS == "darwin" {
		// macOS uses launchd; writing systemd and OpenRC files here (what the
		// generic fallback below does) only left Linux files nobody can use.
		return errDarwinUnsupported
	}
	n := cfg.names()
	unit := GenerateSystemd(systemdConfigFor(n, cfg.ExecPath, cfg.ConfigPath))
	script := GenerateOpenRC(openrcConfigFor(n, cfg.ExecPath, cfg.ConfigPath))
	unitFile, scriptFile := n.Name+".service", n.Name+"-openrc"
	if cfg.DryRun {
		fmt.Fprintln(out, "[dry-run] could not detect init system")
		fmt.Fprintf(out, "[dry-run] would write ./%s and ./%s\n", unitFile, scriptFile)
		return nil
	}
	if err := os.WriteFile(unitFile, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", unitFile, err)
	}
	if err := os.WriteFile(scriptFile, []byte(script), 0o755); err != nil {
		return fmt.Errorf("writing %s: %w", scriptFile, err)
	}
	fmt.Fprintln(out, "Could not detect init system. Files written to current directory.")
	return nil
}

// Uninstall reverses Install for the given (or detected) init system.
func Uninstall(cfg InstallConfig) error {
	initSys := cfg.initSystem()
	out := cfg.out()
	n := cfg.names()
	switch initSys {
	case InitSystemd:
		if cfg.DryRun {
			fmt.Fprintf(out, "[dry-run] would run: systemctl stop %s\n", n.Name)
			fmt.Fprintf(out, "[dry-run] would run: systemctl disable %s\n", n.Name)
			fmt.Fprintf(out, "[dry-run] would remove %s\n", n.Unit)
			fmt.Fprintln(out, "[dry-run] would run: systemctl daemon-reload")
			return nil
		}
		_ = run("systemctl", "stop", n.Name)
		_ = run("systemctl", "disable", n.Name)
		if err := os.Remove(n.Unit); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing systemd unit: %w", err)
		}
		_ = run("systemctl", "daemon-reload")
		fmt.Fprintln(out, "Removed systemd service.")
	case InitOpenRC:
		if cfg.DryRun {
			fmt.Fprintf(out, "[dry-run] would run: rc-service %s stop\n", n.Name)
			fmt.Fprintf(out, "[dry-run] would run: rc-update del %s default\n", n.Name)
			fmt.Fprintf(out, "[dry-run] would remove %s\n", n.InitScript)
			return nil
		}
		_ = run("rc-service", n.Name, "stop")
		_ = run("rc-update", "del", n.Name, "default")
		if err := os.Remove(n.InitScript); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing OpenRC script: %w", err)
		}
		fmt.Fprintln(out, "Removed OpenRC service.")
	case InitWindows:
		fmt.Fprintln(out, "Remove the Windows Service with: nssm remove gubern confirm (or vortex, if installed before the rename)")
		if !cfg.SkipFirewall {
			removeFirewallRule(cfg.DryRun, out)
		}
	default:
		if cfg.DryRun {
			fmt.Fprintf(out, "[dry-run] would remove ./%s.service and ./%s-openrc\n", n.Name, n.Name)
			return nil
		}
		_ = os.Remove(filepath.Clean(n.Name + ".service"))
		_ = os.Remove(filepath.Clean(n.Name + "-openrc"))
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
