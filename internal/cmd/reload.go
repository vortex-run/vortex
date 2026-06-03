package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/pkg/pidfile"
)

// newReloadCommand builds `vortex reload`, which asks a running server to
// re-read its config without restarting. On Unix it sends SIGHUP to the PID in
// the pidfile; on Windows it calls the localhost-only POST /internal/reload
// endpoint. It then reads /health to report the (possibly new) config hash.
func newReloadCommand() *cobra.Command {
	var (
		pidfilePath string
		apiPort     int
	)
	c := &cobra.Command{
		Use:   "reload",
		Short: "Reload configuration without restarting the server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			alive, pid, err := pidfile.IsRunning(pidfilePath)
			if errors.Is(err, os.ErrNotExist) || (err == nil && !alive) {
				fmt.Fprintln(out, "VORTEX is not running")
				return errNotRunning
			}
			if err != nil {
				return fmt.Errorf("checking pidfile: %w", err)
			}

			if err := requestReload(pid, apiPort); err != nil {
				return fmt.Errorf("requesting reload: %w", err)
			}

			// Give the server a moment to re-validate and swap config.
			time.Sleep(2 * time.Second)

			body, err := fetchHealth(apiPort)
			if err != nil {
				return fmt.Errorf("confirming reload via health: %w", err)
			}
			var h statusHealth
			if err := json.Unmarshal(body, &h); err != nil {
				return fmt.Errorf("parsing health response: %w", err)
			}
			fmt.Fprintf(out, "Config reloaded successfully (hash: %s)\n", shortHash(h.ConfigHash))
			return nil
		},
	}
	c.Flags().StringVar(&pidfilePath, "pidfile", "vortex.pid", "path to the PID file")
	c.Flags().IntVar(&apiPort, "api-port", 9090, "management API port")
	return c
}
