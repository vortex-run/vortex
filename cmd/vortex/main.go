// Command vortex is the single entrypoint for the VORTEX platform.
//
// Non-Negotiable Rule #1: everything compiles into one binary. This file is the
// bare-bones M1.1 boot path — it initializes structured logging and the
// lifecycle manager, prints version information, and exits cleanly. The full
// CLI (start/stop/status/reload/version subcommands, M1.3) and the config
// engine (M1.2) are layered on in later milestones.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/vortex-run/vortex/pkg/lifecycle"
	"github.com/vortex-run/vortex/pkg/logger"
)

// Build metadata. These are overridden at build time via -ldflags
// (see Taskfile.yml); the defaults make `go run` and bare `go build` work too.
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
	)
	fs.BoolVar(&showVersion, "version", false, "print version information and exit")
	fs.BoolVar(&jsonLog, "json", false, "emit logs as JSON instead of human-readable text")
	fs.StringVar(&logLevel, "log-level", "info", "log level: debug|info|warn|error")

	if err := fs.Parse(args); err != nil {
		// flag already printed the error/usage.
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

	mgr := lifecycle.New(lifecycle.Config{Logger: log})
	mgr.OnReload("config", func(context.Context) error {
		// M1.2 will re-validate and re-apply vortex.cue here.
		log.Info("reload requested (no config engine yet — M1.2)")
		return nil
	})
	mgr.OnShutdown("runtime", func(context.Context) error {
		log.Info("runtime stopped")
		return nil
	})

	// M1.1 has nothing long-running to host yet, so boot is considered
	// successful and we exit cleanly. Once the API server (M1.7) and other
	// subsystems exist, this becomes mgr.Run(ctx) blocking on signals.
	log.Info("vortex boot complete", "note", "M1.1 scaffold — no services hosted yet")
	mgr.Shutdown()
	return 0
}
