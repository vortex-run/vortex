package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// newVersionCommand builds the `vortex version` subcommand. It prints a
// five-field table by default, or just the bare version string with --short.
// All output goes through cmd.OutOrStdout so it is testable and redirectable.
func newVersionCommand() *cobra.Command {
	var short bool
	c := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if short {
				fmt.Fprintln(out, version)
				return nil
			}
			fmt.Fprintf(out, "Version:    %s\n", version)
			fmt.Fprintf(out, "Commit:     %s\n", commit)
			fmt.Fprintf(out, "Built:      %s\n", date)
			fmt.Fprintf(out, "Go version: %s\n", runtime.Version())
			fmt.Fprintf(out, "OS/Arch:    %s/%s\n", runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
	c.Flags().BoolVar(&short, "short", false, "print only the bare version string")
	return c
}
