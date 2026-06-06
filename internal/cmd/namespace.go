package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/tenancy"
)

// errNamespace signals a namespace-command failure whose detail was printed.
var errNamespace = errors.New("namespace command failed")

// namespaceStorePath returns the on-disk namespace registry path, honouring
// VORTEX_NAMESPACE_STORE and otherwise <user-cache>/vortex/namespaces.json.
func namespaceStorePath() string {
	if override := os.Getenv("VORTEX_NAMESPACE_STORE"); override != "" {
		return override
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "vortex", "namespaces.json")
}

// openNamespaceRegistry loads the namespace registry from disk.
func openNamespaceRegistry() (*tenancy.Registry, string, error) {
	path := namespaceStorePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, path, err
	}
	reg := tenancy.NewRegistry()
	if err := reg.Load(path); err != nil {
		return nil, path, err
	}
	return reg, path, nil
}

// newNamespaceCommand builds `vortex namespace` and its subcommands.
func newNamespaceCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "namespace",
		Short: "Manage tenant namespaces and quotas",
		Args:  cobra.NoArgs,
	}
	c.AddCommand(newNamespaceListCommand())
	c.AddCommand(newNamespaceCreateCommand())
	c.AddCommand(newNamespaceDeleteCommand())
	c.AddCommand(newNamespaceQuotaCommand())
	return c
}

func newNamespaceListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all namespaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, _, err := openNamespaceRegistry()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errNamespace
			}
			list := reg.List("")
			out := cmd.OutOrStdout()
			if len(list) == 0 {
				fmt.Fprintln(out, "no namespaces")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tORG\tROUTES\tSECRETS\tCONNS")
			for _, ns := range list {
				q := ns.Quotas()
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\n",
					ns.ID(), ns.Name(), ns.OrgID(), q.MaxRoutes, q.MaxSecrets, q.MaxConnections)
			}
			_ = tw.Flush()
			return nil
		},
	}
}

func newNamespaceCreateCommand() *cobra.Command {
	var (
		name      string
		org       string
		maxRoutes int
		maxSecret int
		maxConns  int64
		bandwidth int64
	)
	c := &cobra.Command{
		Use:   "create <id>",
		Short: "Create a namespace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if org == "" {
				fmt.Fprintln(cmd.OutOrStderr(), "error: --org is required")
				return errNamespace
			}
			reg, path, err := openNamespaceRegistry()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errNamespace
			}
			id := args[0]
			if name == "" {
				name = id
			}
			if _, err := reg.Create(tenancy.NamespaceConfig{
				ID: id, Name: name, OrgID: org,
				Quotas: tenancy.QuotaConfig{
					MaxRoutes: maxRoutes, MaxSecrets: maxSecret,
					MaxConnections: maxConns, BandwidthMbps: bandwidth,
				},
			}); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errNamespace
			}
			if err := reg.Save(path); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errNamespace
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Namespace %q created\n", id)
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "human-readable name")
	c.Flags().StringVar(&org, "org", "", "organization ID (required)")
	c.Flags().IntVar(&maxRoutes, "max-routes", 100, "maximum routes")
	c.Flags().IntVar(&maxSecret, "max-secrets", 50, "maximum secrets")
	c.Flags().Int64Var(&maxConns, "max-conns", 1000, "maximum concurrent connections")
	c.Flags().Int64Var(&bandwidth, "bandwidth-mbps", 0, "bandwidth limit in Mbps (0 = unlimited)")
	return c
}

func newNamespaceDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a namespace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			fmt.Fprintf(cmd.OutOrStdout(), "Delete namespace %q? [y/N] ", id)
			reader := bufio.NewReader(cmd.InOrStdin())
			line, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(line)) != "y" {
				fmt.Fprintln(cmd.OutOrStdout(), "aborted")
				return nil
			}
			reg, path, err := openNamespaceRegistry()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errNamespace
			}
			if err := reg.Delete(id); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errNamespace
			}
			if err := reg.Save(path); err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errNamespace
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Namespace %q deleted\n", id)
			return nil
		},
	}
}

func newNamespaceQuotaCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "quota <id>",
		Short: "Show quota limits for a namespace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, _, err := openNamespaceRegistry()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errNamespace
			}
			ns, err := reg.Get(args[0])
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errNamespace
			}
			q := ns.Quotas()
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Namespace: %s (%s)\n", ns.ID(), ns.Name())
			fmt.Fprintf(out, "  Max routes:       %d\n", q.MaxRoutes)
			fmt.Fprintf(out, "  Max secrets:      %d\n", q.MaxSecrets)
			fmt.Fprintf(out, "  Max connections:  %d\n", q.MaxConnections)
			fmt.Fprintf(out, "  Bandwidth (Mbps): %s\n", unlimitedIfZero(q.BandwidthMbps))
			return nil
		},
	}
}

// unlimitedIfZero renders 0 as "unlimited".
func unlimitedIfZero(v int64) string {
	if v == 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", v)
}
