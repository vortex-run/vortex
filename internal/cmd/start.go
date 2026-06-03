package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/api"
	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/pkg/lifecycle"
	"github.com/vortex-run/vortex/pkg/logger"
)

// newStartCommand builds `vortex start`, which loads and validates config,
// writes a PID file, starts the management API, wires SIGHUP hot-reload, then
// blocks until a shutdown signal — removing the PID file on the way out.
func newStartCommand() *cobra.Command {
	var pidfile string
	c := &cobra.Command{
		Use:   "start",
		Short: "Start the VORTEX server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStart(cmd.Context(), pidfile)
		},
	}
	c.Flags().StringVar(&pidfile, "pidfile", "vortex.pid", "path to the PID file")
	return c
}

// runStart performs the start sequence. ctx controls shutdown: cancelling it
// (or a SIGTERM/SIGINT) triggers a graceful stop. It is separated from the
// cobra command so tests can drive it with a cancellable context instead of
// blocking on real signals.
func runStart(ctx context.Context, pidfile string) error {
	cfgMgr, err := config.NewManager(flags.configPath, log)
	if err != nil {
		return fmt.Errorf("config invalid, refusing to start: %w", err)
	}
	cfg := cfgMgr.Current()

	if err := writePIDFile(pidfile); err != nil {
		return err
	}

	mgr := lifecycle.New(lifecycle.Config{Logger: log})
	cfgMgr.RegisterReload(mgr)

	apiSrv := api.New(api.DefaultAddr, cfgMgr.Holder(), version, log)
	// Windows-safe control plane: POST /internal/reload and /internal/shutdown
	// stand in for SIGHUP/SIGTERM, which Windows lacks.
	apiSrv.SetReloadFunc(cfgMgr.Reload)
	apiSrv.SetShutdownFunc(mgr.Shutdown)
	apiSrv.Start()
	mgr.OnShutdown("api", func(ctx context.Context) error {
		return apiSrv.Shutdown(ctx)
	})
	mgr.OnShutdown("pidfile", func(context.Context) error {
		return os.Remove(pidfile)
	})

	// Re-derive the logger from the loaded config now that observability
	// settings (level, sink, file, sampling) are known.
	format := logger.FormatText
	if flags.jsonLog {
		format = logger.FormatJSON
	}
	log = logger.New(logger.Config{
		Level:    logger.ParseLevel(cfg.Observability.LogLevel),
		Format:   format,
		Sink:     logger.Sink(cfg.Observability.LogSink),
		Path:     cfg.Observability.LogFile,
		Sampling: cfg.Observability.LogSampling,
	})

	log.Info("VORTEX started",
		"version", version,
		"cluster", cfg.Cluster.Name,
		"api_addr", apiSrv.Addr(),
	)

	mgr.Run(ctx)
	log.Info("VORTEX stopped cleanly")
	return nil
}

// writePIDFile writes the current process ID to path as plain text. The richer
// stale-lock-aware logic lives in pkg/pidfile (used by stop/status/reload).
func writePIDFile(path string) error {
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(path, []byte(pid+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing pidfile %s: %w", path, err)
	}
	return nil
}
