package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sandeepbollavaram/vortex/internal/agents"
	"github.com/sandeepbollavaram/vortex/internal/config"
)

// newCheckCommand builds `gubern check`, which validates the config file named
// by the global --config flag without starting any server. On success it prints
// a per-section checklist and exits 0; on failure it prints each problem with
// field path and file:line:col to stderr and exits 1 (via a returned error).
func newCheckCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Validate gubern.cue without starting the server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(flags.configPath)
			if err != nil {
				printCheckErrors(cmd, err)
				// Returning an error makes Execute exit non-zero; usage is
				// silenced so only our formatted lines show.
				return errCheckFailed
			}
			// agents.local_tools is schema-valid as plain strings but `start`
			// also resolves trusted_root against the work dir (exists, is a
			// directory, contains the work dir). Run the same check here so
			// `check` cannot say "is valid" for a config `start` will refuse.
			localFS, lerr := resolveLocalFSConfig(cfg, resolveWorkingDir())
			if lerr != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "✗ agents.local_tools: %v\n", lerr)
				return errCheckFailed
			}
			printCheckOK(cmd, cfg, localFS)
			return nil
		},
	}
}

// errCheckFailed signals a validation failure whose detail was already printed.
var errCheckFailed = errors.New("config validation failed")

func printCheckOK(cmd *cobra.Command, cfg *config.Config, localFS agents.LocalFSConfig) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "✓ cluster       (%s, %d nodes)\n", cfg.Cluster.Name, len(cfg.Cluster.Nodes))
	fmt.Fprintf(out, "✓ tls           (%s, min %s)\n", cfg.TLS.Provider, cfg.TLS.MinVersion)
	fmt.Fprintf(out, "✓ routes        (%d routes)\n", len(cfg.Routes))
	fmt.Fprintln(out, "✓ security")
	fmt.Fprintf(out, "✓ secrets       (%d keys)\n", len(cfg.Secrets.Keys))
	fmt.Fprintln(out, "✓ observability")
	trusted := localFS.TrustedRoot
	if trusted == "" {
		trusted = "none"
	}
	fmt.Fprintf(out, "✓ agents        (local_tools: trusted_root=%s, auto_mode=%t)\n", trusted, localFS.AutoMode)
	fmt.Fprintf(out, "%s is valid (hash: %s)\n", flags.configPath, shortHash(cfg.Hash()))
}

func printCheckErrors(cmd *cobra.Command, err error) {
	errOut := cmd.OutOrStderr()

	var les config.LoadErrors
	if errors.As(err, &les) {
		for _, e := range les {
			fmt.Fprintln(errOut, formatCheckError(e))
		}
		return
	}
	var le *config.LoadError
	if errors.As(err, &le) {
		fmt.Fprintln(errOut, formatCheckError(le))
		return
	}
	fmt.Fprintf(errOut, "✗ %v\n", err)
}

// formatCheckError renders one LoadError as "✗ <field>: <msg> (<file>:<line>:<col>)".
func formatCheckError(e *config.LoadError) string {
	field := e.Field
	if field == "" {
		field = "config"
	}
	loc := e.Path
	if e.Line > 0 {
		loc = fmt.Sprintf("%s:%d", loc, e.Line)
		if e.Column > 0 {
			loc = fmt.Sprintf("%s:%d", loc, e.Column)
		}
	}
	return fmt.Sprintf("✗ %s: %s (%s)", field, e.Message, loc)
}

// shortHash returns the first 7 hex chars of a config hash, git-style.
func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}
