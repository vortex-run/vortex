package cmd

import (
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
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

func newSecretSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name> <value>",
		Short: "Set a secret value (encrypted)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, value := args[0], args[1]
			a, _, err := openSecretAdapter()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errSecret
			}
			if err := a.Set(cmd.Context(), name, value); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errSecret
			}
			// Audit the operation, never the secret value.
			auditCLI(cmd, "secret.set", name)
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
			a, _, err := openSecretAdapter()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errSecret
			}
			out := cmd.OutOrStdout()
			val, gerr := a.Get(cmd.Context(), name)
			if errors.Is(gerr, os.ErrNotExist) {
				fmt.Fprintf(out, "%s: [not set]\n", name)
				return nil
			}
			if gerr != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", gerr)
				return errSecret
			}
			if reveal {
				fmt.Fprintf(out, "%s: %s\n", name, val)
			} else {
				fmt.Fprintf(out, "%s: [set]\n", name)
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
			a, cfg, err := openSecretAdapter()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errSecret
			}
			stored, err := a.List(cmd.Context())
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
			a, _, err := openSecretAdapter()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errSecret
			}
			if err := a.Delete(cmd.Context(), name); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errSecret
			}
			auditCLI(cmd, "secret.delete", name)
			fmt.Fprintf(cmd.OutOrStdout(), "Secret %q deleted\n", name)
			return nil
		},
	}
}
