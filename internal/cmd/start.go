package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/api"
	"github.com/vortex-run/vortex/internal/audit"
	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/cluster"
	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/internal/observability"
	"github.com/vortex-run/vortex/internal/plugins"
	"github.com/vortex-run/vortex/internal/policy"
	"github.com/vortex-run/vortex/internal/proxy"
	"github.com/vortex-run/vortex/internal/proxy/tcp"
	"github.com/vortex-run/vortex/internal/secrets"
	"github.com/vortex-run/vortex/internal/security"
	"github.com/vortex-run/vortex/internal/tenancy"
	vtls "github.com/vortex-run/vortex/internal/tls"
	"github.com/vortex-run/vortex/pkg/lifecycle"
	"github.com/vortex-run/vortex/pkg/logger"
	"go.opentelemetry.io/otel/trace"
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

	// Audit log: tamper-proof record of security-relevant events. Keyed by the
	// cluster name so the `vortex audit` CLI can verify the same chain.
	auditLog, err := openRuntimeAuditLog(cfg, log)
	if err != nil {
		return fmt.Errorf("initialising audit log: %w", err)
	}
	if auditLog != nil {
		_ = auditLog.Append(ctx, "system", "vortex.start", "server", map[string]any{
			"version": version, "cluster": cfg.Cluster.Name,
		})
		mgr.OnShutdown("audit", func(context.Context) error {
			_ = auditLog.Append(context.Background(), "system", "vortex.stop", "server", nil)
			return nil
		})
	}

	apiSrv := api.New(api.DefaultAddr, cfgMgr.Holder(), version, log)
	apiSrv.SetAuditLog(auditLog)

	// Authentication: load (or start) the API-key store and seed the RBAC roles,
	// then protect the management API. /internal/* stays reachable from localhost
	// without a key (control plane); /api/keys requires an admin key.
	keyStore, keyStorePath := openAPIKeyStore(log)
	rbac := auth.NewRBAC()
	apiSrv.SetAuth(auth.NewAuthMiddleware(keyStore, nil, rbac), keyStore, rbac)
	log.Info("auth middleware enabled", "key_store", keyStorePath, "roles", len(rbac.Roles()))
	mgr.OnShutdown("apikeys", func(context.Context) error {
		if err := keyStore.Save(keyStorePath); err != nil {
			log.Warn("saving API key store failed", "err", err)
		}
		return nil
	})

	// Windows-safe control plane: POST /internal/reload and /internal/shutdown
	// stand in for SIGHUP/SIGTERM, which Windows lacks. Reload goes through the
	// lifecycle so ALL reload hooks fire (config swap + proxy rebuild), not just
	// the config swap.
	apiSrv.SetReloadFunc(func() error { mgr.Reload(); return nil })
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

	// --- secrets: validate declared keys, open store, load injectable env ---
	if _, err := loadSecrets(ctx, cfg, log); err != nil {
		return fmt.Errorf("initialising secrets: %w", err)
	}

	// --- policy: OPA authorization engine (opt-in via VORTEX_POLICY_DIR) -----
	policyEngine, err := buildPolicyEngine(log)
	if err != nil {
		return fmt.Errorf("initialising policy engine: %w", err)
	}
	// Hot-reload policy on SIGHUP / POST /internal/reload alongside the config.
	mgr.OnReload("policy", func(context.Context) error {
		if rerr := policyEngine.Reload(ctx); rerr != nil {
			log.Error("policy reload failed, keeping previous policy", "err", rerr)
		}
		return nil
	})

	// --- security edge: IP blocking + rate limiting at the L7 edge ----------
	edge, err := buildSecurityEdge(ctx, cfg, log)
	if err != nil {
		return fmt.Errorf("initialising security edge: %w", err)
	}

	// --- observability: metrics, tracing, profiling ------------------------
	metrics := observability.NewMetrics("vortex")
	apiSrv.SetMetricsHandler(metrics.Handler())

	tracerProvider, err := observability.NewTracer(ctx, observability.TracerConfig{
		ServiceName: cfg.Cluster.Name,
		Endpoint:    cfg.Observability.TraceEndpoint,
		Enabled:     cfg.Observability.Tracing && cfg.Observability.TraceEndpoint != "",
	})
	if err != nil {
		return fmt.Errorf("initialising tracing: %w", err)
	}
	mgr.OnShutdown("tracer", func(c context.Context) error {
		return observability.ShutdownTracer(c, tracerProvider)
	})
	tracer := tracerProvider.Tracer("vortex/proxy")

	profiler := observability.NewProfiler(observability.ProfilerConfig{
		Enabled: os.Getenv("VORTEX_PPROF") == "true",
	})
	go func() {
		if perr := profiler.Start(ctx); perr != nil {
			log.Warn("profiler stopped with error", "err", perr)
		}
	}()

	log.Info("observability started",
		"tracing", cfg.Observability.Tracing && cfg.Observability.TraceEndpoint != "",
		"profiling", os.Getenv("VORTEX_PPROF") == "true",
		"metrics_path", "/metrics",
	)

	// --- plugins: sandboxed WASM runtime + registry -------------------------
	pluginRuntime, pluginRegistry, err := buildPlugins(ctx, log)
	if err != nil {
		return fmt.Errorf("initialising plugins: %w", err)
	}
	if pluginRuntime != nil {
		mgr.OnShutdown("plugins", func(c context.Context) error { return pluginRuntime.Close(c) })
	}

	// --- tenancy: namespace registry + quota enforcer -----------------------
	tenantRegistry, tenantEnforcer := buildTenancy(log)

	// --- cluster: gossip + raft when multi-node, else single-node mode ------
	clusterMgr, err := buildCluster(ctx, cfg, log)
	if err != nil {
		return fmt.Errorf("initialising cluster: %w", err)
	}
	if clusterMgr != nil {
		mgr.OnShutdown("cluster", func(context.Context) error { return clusterMgr.Shutdown() })
	}

	// --- data plane: TLS manager, connection pool, proxy manager -----------

	tlsMgr, err := buildTLSManager(cfg, log)
	if err != nil {
		return fmt.Errorf("initialising TLS: %w", err)
	}

	// mTLS identity mesh: when any route has mtls:true, set up the cluster CA +
	// node cert rotation and the mTLS config used to wrap those routes.
	mtlsCfg, err := buildMTLS(ctx, cfg, log)
	if err != nil {
		return fmt.Errorf("initialising mTLS: %w", err)
	}

	pool := tcp.NewPool(tcp.PoolConfig{
		MaxIdle:     100,
		MaxOpen:     1000,
		IdleTimeout: 90 * time.Second,
		DialTimeout: 10 * time.Second,
	})
	mgr.OnShutdown("tcp-pool", func(context.Context) error { return pool.Close() })

	// The data plane is held behind a swappable holder so config reload can
	// rebuild all listeners against the new route set.
	dp := &dataPlane{
		ctx: ctx, pool: pool, tls: tlsMgr, mtls: mtlsCfg, policy: policyEngine,
		edge: edge, metrics: metrics, tracer: tracer,
		pluginRT: pluginRuntime, pluginReg: pluginRegistry,
		tenantReg: tenantRegistry, tenantEnf: tenantEnforcer, log: log,
	}
	if err := dp.rebuild(cfgMgr.Holder()); err != nil {
		return fmt.Errorf("initialising proxy manager: %w", err)
	}
	mgr.OnShutdown("proxy", func(c context.Context) error { return dp.stop(c) })

	// On config reload, rebuild the data plane from the new config. This hook
	// runs after the config-swap hook registered by RegisterReload above.
	mgr.OnReload("proxy", func(context.Context) error {
		if rerr := dp.rebuild(cfgMgr.Holder()); rerr != nil {
			log.Error("proxy rebuild after reload failed, keeping previous routes", "err", rerr)
		}
		return nil
	})

	// Surface live route stats on /health from whichever manager is current.
	apiSrv.SetRouteStats(func() []api.RouteHealth {
		stats := dp.stats()
		out := make([]api.RouteHealth, len(stats))
		for i, s := range stats {
			out[i] = api.RouteHealth{Name: s.Name, Protocol: s.Protocol, Listen: s.Listen, Active: s.Active}
		}
		return out
	})

	// Dashboard data providers: extended status, secret set/unset state (never
	// values), and installed plugins.
	wireDashboardProviders(apiSrv, cfgMgr, auditLog, pluginRegistry, policyEngine)
	wireNamespaceHooks(apiSrv, tenantRegistry, tenantEnforcer)

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

// dataPlane holds the currently-running proxy.Manager and rebuilds it on config
// reload. It is safe for concurrent stats reads and reload-driven rebuilds.
type dataPlane struct {
	ctx       context.Context
	pool      *tcp.Pool
	tls       *vtls.Manager
	mtls      *vtls.MTLSConfig
	policy    *policy.Engine
	edge      *security.Edge
	metrics   *observability.Metrics
	tracer    trace.Tracer
	pluginRT  *plugins.Runtime
	pluginReg *plugins.Registry
	tenantReg *tenancy.Registry
	tenantEnf *tenancy.Enforcer
	log       *slog.Logger

	mu      sync.Mutex
	current *proxy.Manager
	cancel  context.CancelFunc
}

// rebuild constructs a proxy.Manager from the current config and starts it,
// stopping any previously-running manager first. On a build error the previous
// manager is left running and the error is returned.
//
// The previous manager is stopped BEFORE the new one starts: routes bind fixed
// ports, so an overlapping start would race the old listener and fail with
// "address already in use", leaving the route with no listener. Stopping first
// frees the ports for a clean rebind. This trades a brief (sub-second) bind gap
// for correctness; in-flight requests on the old listener still drain via the
// listener's own graceful shutdown.
func (d *dataPlane) rebuild(holder *config.Holder) error {
	cfg := holder.Get()
	mgr, err := proxy.NewManager(proxy.ManagerConfig{
		Config: cfg, TLS: d.tls, TCPPool: d.pool, MTLSConfig: d.mtls,
		PolicyEngine: d.policy, Edge: d.edge,
		Metrics: d.metrics, Tracer: d.tracer,
		Runtime: d.pluginRT, PluginRegistry: d.pluginReg,
		Registry: d.tenantReg, Enforcer: d.tenantEnf, Logger: d.log,
	})
	if err != nil {
		return err
	}

	// Swap in the new manager and capture the previous one to stop.
	d.mu.Lock()
	prev, prevCancel := d.current, d.cancel
	runCtx, cancel := context.WithCancel(d.ctx)
	d.current, d.cancel = mgr, cancel
	d.mu.Unlock()

	// Stop the previous manager and wait for its listeners to release their
	// ports before starting the new one on the same ports.
	if prev != nil {
		prevCancel()
		_ = prev.Stop(context.Background())
	}

	go func() {
		if perr := mgr.Start(runCtx); perr != nil {
			d.log.Error("proxy manager stopped with error", "err", perr)
		}
	}()
	return nil
}

// stop cancels and stops the current manager.
func (d *dataPlane) stop(ctx context.Context) error {
	d.mu.Lock()
	mgr, cancel := d.current, d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if mgr != nil {
		return mgr.Stop(ctx)
	}
	return nil
}

// stats returns the current manager's route stats.
func (d *dataPlane) stats() []proxy.RouteStats {
	d.mu.Lock()
	mgr := d.current
	d.mu.Unlock()
	if mgr == nil {
		return nil
	}
	return mgr.Stats()
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

// buildMTLS sets up the cluster CA + node cert rotation and the mTLS config when
// any route has mtls:true, starting the rotation loop. Returns nil when no route
// uses mTLS.
func buildMTLS(ctx context.Context, cfg *config.Config, log *slog.Logger) (*vtls.MTLSConfig, error) {
	if !needsMTLS(cfg) {
		return nil, nil
	}
	// The mTLS store path defaults to the user cache dir but can be overridden
	// via VORTEX_MTLS_STORE so peers (and tests) can share the same cluster CA.
	storePath := os.Getenv("VORTEX_MTLS_STORE")
	if storePath == "" {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			cacheDir = os.TempDir()
		}
		storePath = filepath.Join(cacheDir, "vortex", "mtls")
	}
	store, err := vtls.NewStore(storePath, []byte(cfg.Cluster.Name+"-mtls-key"))
	if err != nil {
		return nil, fmt.Errorf("creating mTLS store: %w", err)
	}

	rm, err := vtls.NewRotationManager(vtls.RotationConfig{
		ClusterName: cfg.Cluster.Name,
		Store:       store,
		Logger:      log,
	})
	if err != nil {
		return nil, fmt.Errorf("creating rotation manager: %w", err)
	}
	rm.StartRotation(ctx)

	mc, err := vtls.NewMTLSConfig(vtls.MTLSConfig{
		RotationMgr: rm,
		TrustDomain: rm.Identity().TrustDomain,
		Logger:      log,
	})
	if err != nil {
		return nil, err
	}
	log.Info("mTLS identity mesh enabled",
		"node_id", rm.Identity().NodeID,
		"trust_domain", rm.Identity().TrustDomain,
		"store", storePath,
	)
	return mc, nil
}

// openAPIKeyStore opens the API-key store, loading any persisted keys. The path
// honours VORTEX_APIKEY_STORE (used by tests/operators) and otherwise defaults
// to <user-cache>/vortex/apikeys.json. A load failure is logged but not fatal —
// the server starts with an empty store rather than refusing to boot.
func openAPIKeyStore(log *slog.Logger) (*auth.APIKeyStore, string) {
	path := os.Getenv("VORTEX_APIKEY_STORE")
	if path == "" {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			cacheDir = os.TempDir()
		}
		path = filepath.Join(cacheDir, "vortex", "apikeys.json")
	}
	// Ensure the parent directory exists so Save on shutdown succeeds.
	if derr := os.MkdirAll(filepath.Dir(path), 0o700); derr != nil {
		log.Warn("creating API key store dir failed", "path", filepath.Dir(path), "err", derr)
	}
	store := auth.NewAPIKeyStore()
	if err := store.Load(path); err != nil {
		log.Warn("loading API key store failed, starting empty", "path", path, "err", err)
	}
	return store, path
}

// openRuntimeAuditLog opens the audit log used by the running server. The path
// (VORTEX_AUDIT_LOG or <cache>/vortex/audit.log) and HMAC key (cluster name)
// match the `vortex audit` CLI so the same chain is verifiable. A failure to
// open is fatal — an unwritable audit log is a security regression, not
// something to silently skip.
func openRuntimeAuditLog(cfg *config.Config, log *slog.Logger) (*audit.Log, error) {
	path := auditLogPath()
	if derr := os.MkdirAll(filepath.Dir(path), 0o700); derr != nil {
		log.Warn("creating audit log dir failed", "path", filepath.Dir(path), "err", derr)
	}
	al, err := audit.NewLog(path, []byte(cfg.Cluster.Name+"-audit-key"))
	if err != nil {
		return nil, err
	}
	log.Info("audit log enabled", "path", path)
	return al, nil
}

// buildCluster starts the gossip+raft cluster manager when the deployment is
// multi-node (cfg.Cluster.Nodes has more than one entry) or VORTEX_BOOTSTRAP is
// set. Single-node deployments skip all clustering overhead and just log the
// mode. The returned manager is nil in single-node mode.
func buildCluster(ctx context.Context, cfg *config.Config, log *slog.Logger) (*cluster.Manager, error) {
	bootstrap := os.Getenv("VORTEX_BOOTSTRAP") == "true"
	if len(cfg.Cluster.Nodes) <= 1 && !bootstrap {
		log.Info("running in single-node mode")
		return nil, nil
	}

	bindAddr := os.Getenv("VORTEX_CLUSTER_BIND")
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}
	node, err := cluster.NewNodeConfig(cfg.Cluster.Name, bindAddr, cfg.Cluster.GossipPort)
	if err != nil {
		return nil, err
	}

	mgr, err := cluster.NewManager(cluster.Config{
		Node:       node,
		RaftPort:   cfg.Cluster.RaftPort,
		GossipPort: cfg.Cluster.GossipPort,
		Bootstrap:  bootstrap,
		Peers:      peersExcludingSelf(cfg.Cluster.Nodes, bindAddr),
		Logger:     log,
	})
	if err != nil {
		return nil, err
	}
	if err := mgr.Start(ctx); err != nil {
		_ = mgr.Shutdown()
		return nil, err
	}
	log.Info("cluster started",
		"node_id", node.NodeID,
		"peers", len(cfg.Cluster.Nodes),
		"leader", mgr.IsLeader(),
	)
	return mgr, nil
}

// peersExcludingSelf returns the configured node addresses minus this node's
// bind address (a node should not try to gossip-join itself).
func peersExcludingSelf(nodes []string, self string) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n != self {
			out = append(out, n)
		}
	}
	return out
}

// wireDashboardProviders attaches the dashboard data providers to the API
// server: extended status, declared-secret set/unset state (never values), and
// installed plugins. Each provider reads live state when called.
func wireDashboardProviders(
	apiSrv *api.Server,
	cfgMgr *config.Manager,
	auditLog *audit.Log,
	pluginRegistry *plugins.Registry,
	policyEngine *policy.Engine,
) {
	apiSrv.SetStatusProvider(func() api.StatusInfo {
		cfg := cfgMgr.Current()
		info := api.StatusInfo{
			ClusterName:   cfg.Cluster.Name,
			TLSProvider:   cfg.TLS.Provider,
			SecretBackend: secretBackendKind(cfg),
			PolicyDefault: policyEngine != nil && policyEngine.UsingDefault(),
		}
		if id, err := vtls.NewNodeIdentity(cfg.Cluster.Name); err == nil {
			info.NodeID = id.NodeID
			info.TrustDomain = id.TrustDomain
		}
		if pluginRegistry != nil {
			info.PluginCount = len(pluginRegistry.List())
		}
		if auditLog != nil {
			if entries, err := auditLog.Query(audit.QueryFilter{}); err == nil {
				info.AuditEntryCount = len(entries)
			}
		}
		return info
	})

	apiSrv.SetSecretsProvider(func() []api.SecretStatus {
		cfg := cfgMgr.Current()
		out := make([]api.SecretStatus, 0, len(cfg.Secrets.Keys))
		ac, err := buildAdapterConfig(cfg)
		if err != nil {
			return out
		}
		adapter, err := secrets.NewAdapter(ac)
		if err != nil {
			return out
		}
		for _, key := range cfg.Secrets.Keys {
			_, gerr := adapter.Get(context.Background(), key)
			out = append(out, api.SecretStatus{Name: key, Set: gerr == nil})
		}
		return out
	})

	apiSrv.SetPluginsProvider(func() []api.PluginInfo {
		if pluginRegistry == nil {
			return nil
		}
		manifests := pluginRegistry.List()
		out := make([]api.PluginInfo, 0, len(manifests))
		for _, m := range manifests {
			hooks := make([]string, 0, len(m.HookTypes))
			for _, h := range m.HookTypes {
				hooks = append(hooks, string(h))
			}
			out = append(out, api.PluginInfo{
				Name: m.Name, Version: m.Version,
				Description: m.Description, HookTypes: hooks,
			})
		}
		return out
	})
}

// wireNamespaceHooks attaches the namespace management endpoints to the API
// server, backed by the tenant registry and enforcer.
func wireNamespaceHooks(apiSrv *api.Server, reg *tenancy.Registry, enf *tenancy.Enforcer) {
	apiSrv.SetNamespaceHooks(
		func() []api.NamespaceInfo {
			out := []api.NamespaceInfo{}
			for _, ns := range reg.List("") {
				out = append(out, nsToAPI(ns.Config()))
			}
			return out
		},
		func(ni api.NamespaceInfo) error {
			_, err := reg.Create(apiToNS(ni))
			if err != nil {
				return err
			}
			return reg.Save(namespaceStorePath())
		},
		func(id string) error {
			if err := reg.Delete(id); err != nil {
				return err
			}
			return reg.Save(namespaceStorePath())
		},
		func(id string) (api.NamespaceStats, bool) {
			if _, err := reg.Get(id); err != nil {
				return api.NamespaceStats{}, false
			}
			s := enf.Stats(id)
			return api.NamespaceStats{
				ActiveConns: s.ActiveConns, BandwidthUsed: s.BandwidthUsed, RouteCount: s.RouteCount,
			}, true
		},
	)
}

// nsToAPI converts a tenancy config to the API shape.
func nsToAPI(c tenancy.NamespaceConfig) api.NamespaceInfo {
	var ni api.NamespaceInfo
	ni.ID, ni.Name, ni.OrgID = c.ID, c.Name, c.OrgID
	ni.Quotas.MaxRoutes = c.Quotas.MaxRoutes
	ni.Quotas.MaxSecrets = c.Quotas.MaxSecrets
	ni.Quotas.MaxConnections = c.Quotas.MaxConnections
	ni.Quotas.BandwidthMbps = c.Quotas.BandwidthMbps
	return ni
}

// apiToNS converts the API shape to a tenancy config.
func apiToNS(ni api.NamespaceInfo) tenancy.NamespaceConfig {
	return tenancy.NamespaceConfig{
		ID: ni.ID, Name: ni.Name, OrgID: ni.OrgID,
		Quotas: tenancy.QuotaConfig{
			MaxRoutes: ni.Quotas.MaxRoutes, MaxSecrets: ni.Quotas.MaxSecrets,
			MaxConnections: ni.Quotas.MaxConnections, BandwidthMbps: ni.Quotas.BandwidthMbps,
		},
	}
}

// secretBackendKind returns the configured secret backend, defaulting to local.
func secretBackendKind(cfg *config.Config) string {
	if cfg.Secrets.Store == "" {
		return "local"
	}
	return cfg.Secrets.Store
}

// buildTenancy opens the namespace registry and builds the quota enforcer.
// Both are always created so routes can declare a namespace_id; a route without
// one incurs no tenancy cost. The registry path honours VORTEX_NAMESPACE_STORE
// (shared with the `vortex namespace` CLI).
func buildTenancy(log *slog.Logger) (*tenancy.Registry, *tenancy.Enforcer) {
	reg := tenancy.NewRegistry()
	path := namespaceStorePath()
	if err := reg.Load(path); err != nil {
		log.Warn("loading namespace registry failed, starting empty", "path", path, "err", err)
	}
	enforcer := tenancy.NewEnforcer(reg)
	log.Info("tenancy enabled", "namespace_count", len(reg.List("")))
	return reg, enforcer
}

// buildPlugins creates the sandboxed WASM runtime and opens the plugin
// registry. Both are always created so routes can declare plugins; a route with
// no plugins incurs no per-request plugin cost. The registry path honours
// VORTEX_PLUGIN_DIR (shared with the `vortex plugin` CLI).
func buildPlugins(ctx context.Context, log *slog.Logger) (*plugins.Runtime, *plugins.Registry, error) {
	rt, err := plugins.NewRuntime(ctx, plugins.RuntimeConfig{})
	if err != nil {
		return nil, nil, err
	}
	reg, err := plugins.NewRegistry(pluginStorePath())
	if err != nil {
		_ = rt.Close(ctx)
		return nil, nil, err
	}
	log.Info("plugin runtime ready", "store", pluginStorePath(), "installed", len(reg.List()))
	return rt, reg, nil
}

// buildSecurityEdge constructs the L7 edge (IP blocking + optional global rate
// limiting) from cfg.Security and starts its background maintenance goroutines.
// It returns nil when no edge protection is configured. block_clouds is a stub:
// it is logged but not yet enforced.
func buildSecurityEdge(ctx context.Context, cfg *config.Config, log *slog.Logger) (*security.Edge, error) {
	sec := cfg.Security
	hasAllowlist := len(sec.IPAllowlist) > 0
	if !hasAllowlist && !sec.BlockTor {
		// Nothing to enforce at the edge; per-route rate limits still apply via
		// the proxy manager independently of the global Edge.
		log.Info("security edge inactive", "reason", "no ip_allowlist or exit-node filtering configured")
		return nil, nil
	}

	bl, err := security.NewBlocklist(security.BlocklistConfig{
		IPAllowlist: sec.IPAllowlist,
		BlockTor:    sec.BlockTor,
	})
	if err != nil {
		return nil, err
	}
	if sec.BlockClouds {
		log.Warn("block_clouds is configured but not yet implemented (no-op)")
	}

	edge := security.NewEdge(security.EdgeConfig{Blocklist: bl})

	// Keep the Tor exit list fresh in the background.
	if sec.BlockTor {
		go bl.StartTorRefresh(ctx, "")
	}

	log.Info("security edge enabled",
		"block_tor", sec.BlockTor,
		"allowlist_size", len(sec.IPAllowlist),
		"auto_ban", false,
	)
	return edge, nil
}

// buildPolicyEngine constructs the OPA policy engine. Policy enforcement is
// opt-in: when VORTEX_POLICY_DIR is unset the engine compiles the built-in
// allow-all policy, so a fresh install proxies all requests. A directory with
// .rego files enables real enforcement.
func buildPolicyEngine(log *slog.Logger) (*policy.Engine, error) {
	policyDir := os.Getenv("VORTEX_POLICY_DIR")
	engine, err := policy.NewEngine(policy.EngineConfig{
		PolicyDir: policyDir,
		QueryPath: "data.vortex.allow",
	})
	if err != nil {
		return nil, err
	}
	log.Info("policy engine loaded", "policy_dir", policyDir, "default", engine.UsingDefault())
	return engine, nil
}

// loadSecrets validates the declared secret keys, opens the secret store, warns
// (non-fatally) about any declared secret that is not yet set, and — when
// inject_env is enabled — resolves the secrets that ARE set into an env map for
// injection into managed processes. Invalid key names or a store that cannot be
// opened are fatal; missing secret values are not (they may be set later).
func loadSecrets(ctx context.Context, cfg *config.Config, log *slog.Logger) (map[string]string, error) {
	if err := secrets.ValidateKeys(cfg.Secrets.Keys); err != nil {
		return nil, err
	}

	ac, err := buildAdapterConfig(cfg)
	if err != nil {
		return nil, err
	}
	adapter, err := secrets.NewAdapter(ac)
	if err != nil {
		return nil, fmt.Errorf("opening secret backend: %w", err)
	}

	// Verify connectivity to the backend. An unreachable external backend is a
	// warning, not a fatal error: secrets may be resolved later, and a transient
	// outage should not prevent the proxy from starting.
	if perr := adapter.Ping(ctx); perr != nil {
		log.Warn("secret backend unreachable at startup", "kind", ac.Kind, "err", perr)
	} else {
		log.Info("secret backend connected", "kind", ac.Kind)
	}

	// Warn about declared-but-unset secrets and collect the ones that are set.
	var present, missing []string
	for _, key := range cfg.Secrets.Keys {
		_, gerr := adapter.Get(ctx, key)
		switch {
		case gerr == nil:
			present = append(present, key)
		case errors.Is(gerr, os.ErrNotExist):
			missing = append(missing, key)
			log.Warn("declared secret not set", "name", key,
				"hint", "run: vortex secret set "+key+" <value>")
		default:
			return nil, fmt.Errorf("checking secret %s: %w", key, gerr)
		}
	}

	if !cfg.Secrets.InjectEnv {
		return nil, nil
	}

	env, err := secrets.ResolveAdapter(ctx, adapter, present)
	if err != nil {
		return nil, err
	}
	log.Info("secrets loaded", "count", len(present), "missing", len(missing))
	return env, nil
}

// needsMTLS reports whether any route has mtls:true.
func needsMTLS(cfg *config.Config) bool {
	for _, r := range cfg.Routes {
		if r.MTLS {
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
