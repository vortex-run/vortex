// Package cmd implements the vortex command-line interface (build plan M1.3),
// built on spf13/cobra. The root command owns the global persistent flags
// (--config, --log-level, --json) and initialises the structured logger in its
// PersistentPreRunE so every subcommand has a ready logger before it runs.
//
// Subcommands (start, stop, status, reload, version) are added one at a time in
// their own files. With no subcommand, the root command boots VORTEX: it loads
// and validates the config, starts the management API, wires SIGHUP hot-reload,
// and blocks until a shutdown signal.
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/api"
	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/pkg/lifecycle"
	"github.com/vortex-run/vortex/pkg/logger"
)

// Build metadata, overridden at build time via -ldflags (see Taskfile.yml).
var (
	version = "0.0.0-dev"
	commit  = "none"
	date    = "unknown"
)

// globalFlags holds values bound to the root command's persistent flags.
type globalFlags struct {
	configPath string
	logLevel   string
	jsonLog    bool
}

// log is the process logger, initialised by PersistentPreRunE and shared by
// subcommands.
var (
	flags globalFlags
	log   *slog.Logger
)

// NewRootCommand constructs the root cobra command with global flags wired up.
// It is exported so tests can build an isolated command tree.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "vortex",
		Short:         "VORTEX — one binary. any server. fully autonomous.",
		Long:          "VORTEX is a self-hosted autonomous platform: a single binary that owns edge, routing, clustering, security, observability, and an AI agent runtime.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Initialise the logger from the global flags before any subcommand
		// (or the root RunE) executes.
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			format := logger.FormatText
			if flags.jsonLog {
				format = logger.FormatJSON
			}
			log = logger.New(logger.Config{
				Level:  logger.ParseLevel(flags.logLevel),
				Format: format,
			})
			return nil
		},
		// With no subcommand, boot VORTEX.
		RunE: func(_ *cobra.Command, _ []string) error {
			return boot()
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&flags.configPath, "config", config.DefaultPath, "path to vortex.cue")
	pf.StringVar(&flags.logLevel, "log-level", "info", "log level: debug|info|warn|error")
	pf.BoolVar(&flags.jsonLog, "json", false, "emit logs as JSON instead of human-readable text")

	root.AddCommand(newVersionCommand())

	return root
}

// Execute runs the root command and exits the process with an appropriate code.
func Execute() {
	if err := NewRootCommand().Execute(); err != nil {
		// PersistentPreRunE may not have run on early parse errors; guard nil.
		if log != nil {
			log.Error("vortex exited with error", "err", err)
		} else {
			fmt.Fprintln(os.Stderr, "vortex:", err)
		}
		os.Exit(1)
	}
}

// boot loads config, starts the management API, wires hot-reload, and blocks
// until a shutdown signal. This is the behaviour previously in main.run().
func boot() error {
	log.Info("vortex starting",
		"version", version,
		"commit", commit,
		"go", runtime.Version(),
		"os", runtime.GOOS,
		"arch", runtime.GOARCH,
	)

	cfgMgr, err := config.NewManager(flags.configPath, log)
	if err != nil {
		return fmt.Errorf("config invalid, refusing to start: %w", err)
	}
	cfg := cfgMgr.Current()
	log.Info("config loaded",
		"cluster", cfg.Cluster.Name,
		"routes", len(cfg.Routes),
		"hash", cfg.Hash(),
	)
	// Re-derive the logger using the config's log level now that it is known.
	format := logger.FormatText
	if flags.jsonLog {
		format = logger.FormatJSON
	}
	log = logger.New(logger.Config{Level: logger.ParseLevel(cfg.Observability.LogLevel), Format: format})

	mgr := lifecycle.New(lifecycle.Config{Logger: log})
	cfgMgr.RegisterReload(mgr)

	apiSrv := api.New(api.DefaultAddr, cfgMgr.Holder(), version, log)
	apiSrv.Start()
	mgr.OnShutdown("api", func(ctx context.Context) error {
		return apiSrv.Shutdown(ctx)
	})

	log.Info("vortex boot complete", "api_addr", apiSrv.Addr())
	mgr.Run(context.Background())
	return nil
}
