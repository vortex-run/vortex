package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/plugins"
)

// errPlugin signals a plugin-command failure whose detail was already printed.
var errPlugin = errors.New("plugin command failed")

// pluginStorePath returns the on-disk plugin registry path, honouring
// VORTEX_PLUGIN_DIR and otherwise defaulting to <user-cache>/vortex/plugins.
func pluginStorePath() string {
	if override := os.Getenv("VORTEX_PLUGIN_DIR"); override != "" {
		return override
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "vortex", "plugins")
}

// openRegistry opens the plugin registry.
func openRegistry() (*plugins.Registry, error) {
	return plugins.NewRegistry(pluginStorePath())
}

// newPluginCommand builds `vortex plugin` with list/install/remove/info.
func newPluginCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "plugin",
		Short: "Manage WASM plugins",
		Args:  cobra.NoArgs,
	}
	c.AddCommand(newPluginListCommand())
	c.AddCommand(newPluginInstallCommand())
	c.AddCommand(newPluginRemoveCommand())
	c.AddCommand(newPluginInfoCommand())
	return c
}

func newPluginListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed plugins",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, err := openRegistry()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errPlugin
			}
			list := reg.List()
			out := cmd.OutOrStdout()
			if len(list) == 0 {
				fmt.Fprintln(out, "no plugins installed")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tVERSION\tHOOKS")
			for _, m := range list {
				hooks := make([]string, 0, len(m.HookTypes))
				for _, h := range m.HookTypes {
					hooks = append(hooks, string(h))
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", m.Name, m.Version, strings.Join(hooks, ","))
			}
			_ = tw.Flush()
			return nil
		},
	}
}

func newPluginInstallCommand() *cobra.Command {
	var (
		name    string
		version string
	)
	c := &cobra.Command{
		Use:   "install <path>",
		Short: "Install a WASM plugin from a local file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			wasm, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errPlugin
			}
			reg, err := openRegistry()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errPlugin
			}
			if name == "" {
				name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			}
			if version == "" {
				version = "0.1.0"
			}
			manifest := plugins.PluginManifest{
				Name: name, Version: version, Checksum: reg.Checksum(wasm),
				Description: "installed via vortex plugin install",
			}
			if err := reg.Install(manifest, wasm); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errPlugin
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Plugin %s v%s installed\n", name, version)
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "plugin name (default: file base name)")
	c.Flags().StringVar(&version, "version", "", "plugin version (default: 0.1.0)")
	return c
}

func newPluginRemoveCommand() *cobra.Command {
	var version string
	c := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			reg, err := openRegistry()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errPlugin
			}
			if version == "" {
				_, m, gerr := reg.Get(name, "latest")
				if gerr != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", gerr)
					return errPlugin
				}
				version = m.Version
			}
			if err := reg.Remove(name, version); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errPlugin
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Plugin %s removed\n", name)
			return nil
		},
	}
	c.Flags().StringVar(&version, "version", "", "version to remove (default: latest)")
	return c
}

func newPluginInfoCommand() *cobra.Command {
	var version string
	c := &cobra.Command{
		Use:   "info <name>",
		Short: "Show a plugin's manifest details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			reg, err := openRegistry()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errPlugin
			}
			ver := version
			if ver == "" {
				ver = "latest"
			}
			_, m, err := reg.Get(name, ver)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errPlugin
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Name:        %s\n", m.Name)
			fmt.Fprintf(out, "Version:     %s\n", m.Version)
			fmt.Fprintf(out, "Description: %s\n", m.Description)
			fmt.Fprintf(out, "Checksum:    %s\n", m.Checksum)
			hooks := make([]string, 0, len(m.HookTypes))
			for _, h := range m.HookTypes {
				hooks = append(hooks, string(h))
			}
			fmt.Fprintf(out, "Hooks:       %s\n", strings.Join(hooks, ", "))
			return nil
		},
	}
	c.Flags().StringVar(&version, "version", "", "version to inspect (default: latest)")
	return c
}
