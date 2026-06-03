// Command vortex is the single entrypoint for the VORTEX platform.
//
// Non-Negotiable Rule #1: everything compiles into one binary. As of M1.2 the
// boot path initializes structured logging, loads and validates the CUE config
// (failing fast with file:line on error — Rule #3), starts the management API,
// wires SIGHUP-driven config hot-reload, and blocks until a shutdown signal.
// The full Cobra CLI (start/stop/status/reload/version) arrives in M1.3.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"

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

func main() {
	os.Exit(run(os.Args[1:]))
}

// run holds the real logic so it can return an exit code and be exercised by
// tests without terminating the test process.
func run(args []string) int {
	fs := flag.NewFlagSet("vortex", flag.ContinueOnError)
	var (
		showVersion bool
		jsonLog     bool
		logLevel    string
		configPath  string
		apiAddr     string
		check       bool
	)
	fs.BoolVar(&showVersion, "version", false, "print version information and exit")
	fs.BoolVar(&jsonLog, "json", false, "emit logs as JSON instead of human-readable text")
	fs.StringVar(&logLevel, "log-level", "info", "log level: debug|info|warn|error")
	fs.StringVar(&configPath, "config", config.DefaultPath, "path to vortex.cue")
	fs.StringVar(&apiAddr, "api-addr", api.DefaultAddr, "management API listen address")
	fs.BoolVar(&check, "check", false, "load and validate config, then exit (no services started)")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if showVersion {
		fmt.Printf("vortex %s (commit %s, built %s, %s/%s, %s)\n",
			version, commit, date, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return 0
	}

	format := logger.FormatText
	if jsonLog {
		format = logger.FormatJSON
	}
	log := logger.New(logger.Config{
		Level:  logger.ParseLevel(logLevel),
		Format: format,
	})

	log.Info("vortex starting",
		"version", version,
		"commit", commit,
		"go", runtime.Version(),
		"os", runtime.GOOS,
		"arch", runtime.GOARCH,
	)

	// Load config before starting any subsystem. Invalid config aborts boot
	// with a non-zero exit (Rule #3).
	cfgMgr, err := config.NewManager(configPath, log)
	if err != nil {
		log.Error("config invalid, refusing to start", "path", configPath, "err", err)
		return 1
	}
	cfg := cfgMgr.Current()
	log.Info("config loaded",
		"cluster", cfg.Cluster.Name,
		"routes", len(cfg.Routes),
		"hash", cfg.Hash(),
	)
	// Re-apply the log level from config now that it is available.
	log = logger.New(logger.Config{Level: logger.ParseLevel(cfg.Observability.LogLevel), Format: format})

	if check {
		log.Info("config check passed", "cluster", cfg.Cluster.Name, "hash", cfg.Hash())
		return 0
	}

	mgr := lifecycle.New(lifecycle.Config{Logger: log})
	cfgMgr.RegisterReload(mgr)

	apiSrv := api.New(apiAddr, cfgMgr.Holder(), version, log)
	apiSrv.Start()
	mgr.OnShutdown("api", func(ctx context.Context) error {
		return apiSrv.Shutdown(ctx)
	})

	log.Info("vortex boot complete", "api_addr", apiSrv.Addr())

	// Block until SIGTERM/SIGINT (shutdown) or SIGHUP (reload, handled by the
	// registered hook). Run executes shutdown hooks before returning.
	mgr.Run(context.Background())
	return 0
}
