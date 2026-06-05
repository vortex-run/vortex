package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/internal/secrets"
)

// errSecret signals a secret-command failure whose detail was already printed;
// Execute exits 1 without an extra generic log line.
var errSecret = errors.New("secret command failed")

// newSecretCommand builds `vortex secret` and its set/get/list/delete
// subcommands for managing the encrypted secret store.
func newSecretCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "secret",
		Short: "Manage VORTEX secrets (encrypted at rest)",
		Args:  cobra.NoArgs,
	}
	c.AddCommand(newSecretSetCommand())
	c.AddCommand(newSecretGetCommand())
	c.AddCommand(newSecretListCommand())
	c.AddCommand(newSecretDeleteCommand())
	return c
}

// openSecretStore loads the config and opens the secret store for its cluster.
func openSecretStore() (*secrets.SecretStore, *config.Config, error) {
	cfg, err := config.Load(flags.configPath)
	if err != nil {
		return nil, nil, err
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	if override := os.Getenv("VORTEX_SECRET_STORE"); override != "" {
		cacheDir = override
		store, serr := secrets.NewSecretStore(cacheDir, []byte(cfg.Cluster.Name+"-secrets"))
		return store, cfg, serr
	}
	path := filepath.Join(cacheDir, "vortex", "secrets")
	store, err := secrets.NewSecretStore(path, []byte(cfg.Cluster.Name+"-secrets"))
	return store, cfg, err
}

func newSecretSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name> <value>",
		Short: "Set a secret value (encrypted)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, value := args[0], args[1]
			store, _, err := openSecretStore()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errSecret
			}
			if err := store.Set(name, value); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errSecret
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Secret %q set successfully\n", name)
			return nil
		},
	}
}

func newSecretGetCommand() *cobra.Command {
	var reveal bool
	c := &cobra.Command{
		Use:   "get <name>",
		Short: "Show whether a secret is set (use --reveal to print its value)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			store, _, err := openSecretStore()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errSecret
			}
			out := cmd.OutOrStdout()
			if reveal {
				val, gerr := store.Get(name)
				if errors.Is(gerr, os.ErrNotExist) {
					fmt.Fprintf(out, "%s: [not set]\n", name)
					return nil
				}
				if gerr != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", gerr)
					return errSecret
				}
				fmt.Fprintf(out, "%s: %s\n", name, val)
				return nil
			}
			exists, eerr := store.Exists(name)
			if eerr != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", eerr)
				return errSecret
			}
			if exists {
				fmt.Fprintf(out, "%s: [set]\n", name)
			} else {
				fmt.Fprintf(out, "%s: [not set]\n", name)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&reveal, "reveal", false, "print the actual secret value (use with caution)")
	return c
}

func newSecretListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List declared secrets and their set/unset status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, cfg, err := openSecretStore()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errSecret
			}
			stored, err := store.List()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errSecret
			}
			storedSet := make(map[string]bool, len(stored))
			for _, n := range stored {
				storedSet[n] = true
			}

			out := cmd.OutOrStdout()
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)

			fmt.Fprintln(out, "Declared in config:")
			declared := make(map[string]bool, len(cfg.Secrets.Keys))
			for _, k := range cfg.Secrets.Keys {
				declared[k] = true
				status := "[not set]"
				if storedSet[k] {
					status = "[set]"
				}
				fmt.Fprintf(tw, "  %s:\t%s\n", k, status)
			}
			_ = tw.Flush()

			// Any stored secrets not declared in config.
			var extra []string
			for _, n := range stored {
				if !declared[n] {
					extra = append(extra, n)
				}
			}
			if len(extra) > 0 {
				fmt.Fprintln(out, "Extra (not in config):")
				tw2 := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				for _, n := range extra {
					fmt.Fprintf(tw2, "  %s:\t[set]\n", n)
				}
				_ = tw2.Flush()
			}
			return nil
		},
	}
}

func newSecretDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a secret (idempotent)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			store, _, err := openSecretStore()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errSecret
			}
			if err := store.Delete(name); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errSecret
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Secret %q deleted\n", name)
			return nil
		},
	}
}
