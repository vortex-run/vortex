package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/api"
	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/internal/proxy"
	"github.com/vortex-run/vortex/internal/proxy/tcp"
	vtls "github.com/vortex-run/vortex/internal/tls"
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

	// --- data plane: TLS manager, connection pool, proxy manager -----------

	tlsMgr, err := buildTLSManager(cfg, log)
	if err != nil {
		return fmt.Errorf("initialising TLS: %w", err)
	}

	pool := tcp.NewPool(tcp.PoolConfig{
		MaxIdle:     100,
		MaxOpen:     1000,
		IdleTimeout: 90 * time.Second,
		DialTimeout: 10 * time.Second,
	})
	mgr.OnShutdown("tcp-pool", func(context.Context) error { return pool.Close() })

	proxyMgr, err := proxy.NewManager(proxy.ManagerConfig{
		Config:  cfg,
		TLS:     tlsMgr,
		TCPPool: pool,
		Logger:  log,
	})
	if err != nil {
		return fmt.Errorf("initialising proxy manager: %w", err)
	}

	// Run the data plane under the lifecycle ctx; stop it on shutdown.
	proxyCtx, proxyCancel := context.WithCancel(ctx)
	go func() {
		if perr := proxyMgr.Start(proxyCtx); perr != nil {
			log.Error("proxy manager stopped with error", "err", perr)
		}
	}()
	mgr.OnShutdown("proxy", func(c context.Context) error {
		proxyCancel()
		return proxyMgr.Stop(c)
	})

	// Surface live route stats on /health.
	apiSrv.SetRouteStats(func() []api.RouteHealth {
		stats := proxyMgr.Stats()
		out := make([]api.RouteHealth, len(stats))
		for i, s := range stats {
			out[i] = api.RouteHealth{Name: s.Name, Protocol: s.Protocol, Listen: s.Listen, Active: s.Active}
		}
		return out
	})

	// ACME HTTP-01 challenge handler must be reachable on :80 for cert issuance.
	if h := tlsChallengeHandler(tlsMgr, cfg); h != nil {
		startChallengeServer(mgr, h, log)
	}

	log.Info("VORTEX started",
		"version", version,
		"cluster", cfg.Cluster.Name,
		"api_addr", apiSrv.Addr(),
		"routes", len(cfg.Routes),
	)

	mgr.Run(ctx)
	log.Info("VORTEX stopped cleanly")
	return nil
}

// buildTLSManager creates a vtls.Manager when any route needs TLS (https/h3),
// or returns nil when none do.
func buildTLSManager(cfg *config.Config, log *slog.Logger) (*vtls.Manager, error) {
	if !needsTLS(cfg) {
		return nil, nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	storePath := filepath.Join(cacheDir, "vortex", "certs")
	storeKey := []byte(cfg.Cluster.Name + "-tls-key")

	log.Info("initialising TLS manager", "provider", cfg.TLS.Provider, "store", storePath)
	return vtls.NewManager(vtls.ManagerConfig{
		Provider:   cfg.TLS.Provider,
		StorePath:  storePath,
		StoreKey:   storeKey,
		MinVersion: cfg.TLS.MinVersion,
		ACME: vtls.ACMEConfig{
			Email:   cfg.TLS.ACMEEmail,
			Staging: false,
		},
	})
}

// needsTLS reports whether any route uses a TLS-bearing protocol.
func needsTLS(cfg *config.Config) bool {
	for _, r := range cfg.Routes {
		if r.Protocol == "https" || r.Protocol == "h3" {
			return true
		}
	}
	return false
}

// tlsChallengeHandler returns the ACME HTTP-01 challenge handler for ACME
// providers, or nil for the internal CA / no TLS.
func tlsChallengeHandler(tlsMgr *vtls.Manager, cfg *config.Config) http.Handler {
	if tlsMgr == nil {
		return nil
	}
	if cfg.TLS.Provider != "letsencrypt" && cfg.TLS.Provider != "zerossl" {
		return nil
	}
	return tlsMgr.ChallengeHandler()
}

// startChallengeServer serves the ACME HTTP-01 challenge handler on :80 and
// registers its shutdown with the lifecycle manager.
func startChallengeServer(mgr *lifecycle.Manager, h http.Handler, log *slog.Logger) {
	srv := &http.Server{Addr: ":80", Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Warn("ACME challenge server stopped", "err", err)
		}
	}()
	mgr.OnShutdown("acme-challenge", func(c context.Context) error { return srv.Shutdown(c) })
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
