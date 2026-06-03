package cmd

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/pkg/pidfile"
)

// newStopCommand builds `vortex stop`, which signals a running server to shut
// down gracefully and waits for it to exit. On Unix it sends SIGTERM to the PID
// in the pidfile; on Windows (no SIGTERM) it calls the localhost-only
// POST /internal/shutdown endpoint. It then polls IsRunning until the process
// is gone or --timeout elapses.
func newStopCommand() *cobra.Command {
	var (
		pidfilePath string
		timeout     time.Duration
		apiPort     int
	)
	c := &cobra.Command{
		Use:   "stop",
		Short: "Stop the running VORTEX server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			alive, pid, err := pidfile.IsRunning(pidfilePath)
			if errors.Is(err, os.ErrNotExist) || (!alive && err == nil) {
				fmt.Fprintln(out, "VORTEX is not running")
				return nil
			}
			if err != nil {
				return fmt.Errorf("checking pidfile: %w", err)
			}

			if err := requestStop(pid, apiPort); err != nil {
				return fmt.Errorf("requesting shutdown: %w", err)
			}

			deadline := time.Now().Add(timeout)
			for time.Now().Before(deadline) {
				if alive, _, _ := pidfile.IsRunning(pidfilePath); !alive {
					fmt.Fprintln(out, "VORTEX stopped")
					return nil
				}
				time.Sleep(500 * time.Millisecond)
			}
			return fmt.Errorf("VORTEX did not stop within %s", timeout)
		},
	}
	c.Flags().StringVar(&pidfilePath, "pidfile", "vortex.pid", "path to the PID file")
	c.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "how long to wait for the server to stop")
	c.Flags().IntVar(&apiPort, "api-port", 9090, "management API port (used for the Windows shutdown path)")
	return c
}
