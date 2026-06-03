package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/service"
)

// errNeedRoot signals that a privileged operation was attempted without root;
// Execute exits 1 without an extra generic log line.
var errNeedRoot = errors.New("must run as root")

// newServiceCommand builds `vortex service` and its install/uninstall/generate
// subcommands for managing VORTEX as a system service.
func newServiceCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "service",
		Short: "Manage VORTEX as a system service",
		Args:  cobra.NoArgs,
	}
	c.AddCommand(newServiceInstallCommand())
	c.AddCommand(newServiceUninstallCommand())
	c.AddCommand(newServiceGenerateCommand())
	return c
}

// resolveExecPath returns the given path or, if empty, the running binary's
// absolute path.
func resolveExecPath(p string) (string, error) {
	if p != "" {
		return filepath.Abs(p)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolving executable path: %w", err)
	}
	return exe, nil
}

// resolveConfigPath returns the given path or, if empty, the absolute form of
// the global --config flag.
func resolveConfigPath(p string) (string, error) {
	if p == "" {
		p = flags.configPath
	}
	return filepath.Abs(p)
}

func newServiceInstallCommand() *cobra.Command {
	var (
		execPath   string
		configPath string
		initSystem string
		dryRun     bool
	)
	c := &cobra.Command{
		Use:   "install",
		Short: "Install VORTEX as a system service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			if !dryRun && !isRoot() {
				fmt.Fprintln(cmd.OutOrStderr(), "error: 'vortex service install' must run as root (or use --dry-run)")
				return errNeedRoot
			}

			exec, err := resolveExecPath(execPath)
			if err != nil {
				return err
			}
			conf, err := resolveConfigPath(configPath)
			if err != nil {
				return err
			}
			return service.Install(service.InstallConfig{
				ExecPath:   exec,
				ConfigPath: conf,
				InitSystem: service.InitSystem(initSystem),
				DryRun:     dryRun,
				Out:        out,
			})
		},
	}
	c.Flags().StringVar(&execPath, "exec-path", "", "path to the vortex binary (default: this binary)")
	c.Flags().StringVar(&configPath, "config-path", "", "path to vortex.cue (default: the --config value)")
	c.Flags().StringVar(&initSystem, "init-system", "", "force init system: systemd|openrc|windows (default: auto-detect)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print actions without executing them")
	return c
}

func newServiceUninstallCommand() *cobra.Command {
	var (
		initSystem string
		dryRun     bool
	)
	c := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove VORTEX system service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dryRun && !isRoot() {
				fmt.Fprintln(cmd.OutOrStderr(), "error: 'vortex service uninstall' must run as root (or use --dry-run)")
				return errNeedRoot
			}
			return service.Uninstall(service.InstallConfig{
				InitSystem: service.InitSystem(initSystem),
				DryRun:     dryRun,
				Out:        cmd.OutOrStdout(),
			})
		},
	}
	c.Flags().StringVar(&initSystem, "init-system", "", "force init system: systemd|openrc|windows (default: auto-detect)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print actions without executing them")
	return c
}

func newServiceGenerateCommand() *cobra.Command {
	var (
		format     string
		execPath   string
		configPath string
	)
	c := &cobra.Command{
		Use:   "generate",
		Short: "Print the service unit file without installing",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			exec, err := resolveExecPath(execPath)
			if err != nil {
				return err
			}
			conf, err := resolveConfigPath(configPath)
			if err != nil {
				return err
			}

			f := service.InitSystem(format)
			if f == "" {
				f = service.DetectInitSystem()
			}
			out := cmd.OutOrStdout()
			// "logrotate" is not an init system but a valid generate target.
			if format == "logrotate" {
				fmt.Fprint(out, service.GenerateLogrotate(service.DefaultLogrotateConfig()))
				return nil
			}
			switch f {
			case service.InitOpenRC:
				fmt.Fprint(out, service.GenerateOpenRC(service.DefaultOpenRCConfig(exec, conf)))
			case service.InitSystemd:
				fmt.Fprint(out, service.GenerateSystemd(service.DefaultSystemdConfig(exec, conf)))
			default:
				// On hosts where detection yields windows/unknown, default to
				// systemd output (the most common deployment target).
				fmt.Fprint(out, service.GenerateSystemd(service.DefaultSystemdConfig(exec, conf)))
			}
			return nil
		},
	}
	c.Flags().StringVar(&format, "format", "", "output format: systemd|openrc|logrotate (default: auto-detect init system)")
	c.Flags().StringVar(&execPath, "exec-path", "", "path to the vortex binary (default: this binary)")
	c.Flags().StringVar(&configPath, "config-path", "", "path to vortex.cue (default: the --config value)")
	return c
}
