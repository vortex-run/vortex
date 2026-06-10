package cmd

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

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

func newSecretSetCommand() *cobra.Command {
	var (
		expiresIn   string
		rotateEvery string
	)
	c := &cobra.Command{
		Use:   "set <name> <value>",
		Short: "Set a secret value (encrypted)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, value := args[0], args[1]
			errOut := cmd.OutOrStderr()

			// Lifecycle flags (M19) are tracked by the local encrypted store
			// only; external backends (Vault/SSM/GCP) manage their own TTLs.
			var meta secrets.SecretMetadata
			withMeta := expiresIn != "" || rotateEvery != ""
			if expiresIn != "" {
				d, err := parseLifetime(expiresIn)
				if err != nil {
					fmt.Fprintf(errOut, "error: --expires-in: %v\n", err)
					return errSecret
				}
				meta.ExpiresAt = time.Now().Add(d)
			}
			if rotateEvery != "" {
				d, err := parseLifetime(rotateEvery)
				if err != nil {
					fmt.Fprintf(errOut, "error: --rotate-every: %v\n", err)
					return errSecret
				}
				meta.RotateEvery = d
			}

			a, cfg, err := openSecretAdapter()
			if err != nil {
				fmt.Fprintf(errOut, "error: %v\n", err)
				return errSecret
			}

			if withMeta {
				ac, err := buildAdapterConfig(cfg)
				if err != nil || ac.Local == nil {
					fmt.Fprintf(errOut, "error: --expires-in/--rotate-every require the local secret backend\n")
					return errSecret
				}
				if err := ac.Local.SetWithMetadata(name, value, meta); err != nil {
					fmt.Fprintf(errOut, "error: %v\n", err)
					return errSecret
				}
			} else if err := a.Set(cmd.Context(), name, value); err != nil {
				fmt.Fprintf(errOut, "error: %v\n", err)
				return errSecret
			}
			// Audit the operation, never the secret value.
			auditCLI(cmd, "secret.set", name)
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Secret %q set successfully\n", name)
			if !meta.ExpiresAt.IsZero() {
				fmt.Fprintf(out, "Expires: %s\n", meta.ExpiresAt.Format("2006-01-02"))
			}
			if meta.RotateEvery > 0 {
				fmt.Fprintf(out, "Rotate every: %s\n", rotateEvery)
			}
			return nil
		},
	}
	c.Flags().StringVar(&expiresIn, "expires-in", "", "expiry from now (e.g. 90d, 1y); alerts fire when passed")
	c.Flags().StringVar(&rotateEvery, "rotate-every", "", "rotation interval (e.g. 30d); alerts fire when due")
	return c
}

// parseLifetime parses a duration that may use d (days), w (weeks), or y
// (years) suffixes in addition to Go's native units (e.g. "90d", "1y", "36h").
func parseLifetime(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	var unit time.Duration
	switch {
	case strings.HasSuffix(s, "d"):
		unit = 24 * time.Hour
	case strings.HasSuffix(s, "w"):
		unit = 7 * 24 * time.Hour
	case strings.HasSuffix(s, "y"):
		unit = 365 * 24 * time.Hour
	default:
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q (use e.g. 90d, 12w, 1y, 36h)", s)
		}
		if d <= 0 {
			return 0, fmt.Errorf("duration must be positive, got %q", s)
		}
		return d, nil
	}
	n, err := strconv.ParseFloat(strings.TrimSuffix(s, s[len(s)-1:]), 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid duration %q (use e.g. 90d, 12w, 1y, 36h)", s)
	}
	return time.Duration(n * float64(unit)), nil
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
