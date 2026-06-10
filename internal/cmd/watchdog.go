package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/healing"
)

// newWatchdogCommand builds `vortex watchdog`, a separate supervisor process
// that watches the main vortex and restarts it if it dies.
func newWatchdogCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "watchdog",
		Short: "Supervise the VORTEX process and restart it if it dies",
	}
	c.AddCommand(newWatchdogStartCommand())
	c.AddCommand(newWatchdogStatusCommand())
	return c
}

// newWatchdogStartCommand runs the watchdog loop.
func newWatchdogStartCommand() *cobra.Command {
	var pidFile, binary, configPath string
	var maxRestarts int
	var notify bool
	c := &cobra.Command{
		Use:   "start",
		Short: "Start watching VORTEX (runs until Ctrl+C)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if binary == "" {
				binary, _ = os.Executable()
			}
			w := healing.NewWatchdog(healing.WatchdogConfig{
				PIDFile:         pidFile,
				BinaryPath:      binary,
				ConfigPath:      configPath,
				MaxRestarts:     maxRestarts,
				NotifyOnRestart: notify,
			})
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			fmt.Printf("watchdog: supervising vortex (pidfile=%s, max-restarts=%d/hr)\n", pidFile, maxRestarts)
			err := w.Watch(ctx)
			if ctx.Err() != nil {
				return nil // clean shutdown on signal
			}
			return err
		},
	}
	c.Flags().StringVar(&pidFile, "pid-file", "vortex.pid", "VORTEX pidfile to watch")
	c.Flags().StringVar(&binary, "binary", "", "path to the vortex binary (default: this binary)")
	c.Flags().StringVar(&configPath, "config", "vortex.cue", "vortex config path to start with")
	c.Flags().IntVar(&maxRestarts, "max-restarts", 10, "max restarts per hour before giving up")
	c.Flags().BoolVar(&notify, "notify", false, "send a Telegram alert on restart")
	return c
}

// newWatchdogStatusCommand reports whether vortex is running.
func newWatchdogStatusCommand() *cobra.Command {
	var pidFile string
	c := &cobra.Command{
		Use:   "status",
		Short: "Show whether VORTEX is running",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			alive := healing.PidfileAlive(pidFile)
			if alive {
				fmt.Printf("vortex: running (pidfile=%s)\n", pidFile)
			} else {
				fmt.Printf("vortex: NOT running (pidfile=%s)\n", pidFile)
			}
			return nil
		},
	}
	c.Flags().StringVar(&pidFile, "pid-file", "vortex.pid", "VORTEX pidfile to check")
	return c
}
