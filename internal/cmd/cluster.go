package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/cluster"
	"github.com/vortex-run/vortex/internal/config"
)

// errCluster signals a cluster-command failure whose detail was already printed.
var errCluster = errors.New("cluster command failed")

// newClusterCommand builds `vortex cluster` with status/join/leave subcommands.
func newClusterCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "cluster",
		Short: "Inspect and manage cluster membership",
		Args:  cobra.NoArgs,
	}
	c.AddCommand(newClusterStatusCommand())
	c.AddCommand(newClusterJoinCommand())
	c.AddCommand(newClusterLeaveCommand())
	return c
}

// clusterMode reports whether the loaded config describes a multi-node cluster.
func clusterMode(cfg *config.Config) (multiNode bool) {
	return len(cfg.Cluster.Nodes) > 1 || os.Getenv("VORTEX_BOOTSTRAP") == "true"
}

func newClusterStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show node ID, mode, and configured members",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(flags.configPath)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errCluster
			}
			node, err := cluster.NewNodeConfig(cfg.Cluster.Name, "127.0.0.1", cfg.Cluster.GossipPort)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errCluster
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Node ID:  %s\n", node.NodeID)
			fmt.Fprintf(out, "Cluster:  %s\n", cfg.Cluster.Name)
			if clusterMode(cfg) {
				fmt.Fprintf(out, "Mode:     multi-node\n")
				fmt.Fprintf(out, "Members:  %d configured\n", len(cfg.Cluster.Nodes))
				for _, n := range cfg.Cluster.Nodes {
					fmt.Fprintf(out, "  - %s\n", n)
				}
			} else {
				fmt.Fprintf(out, "Mode:     single-node\n")
			}
			return nil
		},
	}
}

func newClusterJoinCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "join <addr>",
		Short: "Join the cluster at the given peer address",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addr := args[0]
			// Joining a live cluster requires the running server's gossip layer;
			// the CLI records the intent and instructs the operator to add the peer
			// to the node list and reload. (A live join RPC lands in a later
			// milestone alongside the cluster control endpoint.)
			fmt.Fprintf(cmd.OutOrStdout(),
				"To join %s: add it to cluster.nodes in vortex.cue and reload, or start this node with VORTEX_CLUSTER_BIND set and the peer in nodes.\n",
				addr)
			return nil
		},
	}
}

func newClusterLeaveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "leave",
		Short: "Leave the cluster gracefully",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(),
				"To leave the cluster, stop this node with `vortex stop`; it broadcasts a graceful gossip leave on shutdown.\n")
			return nil
		},
	}
}
