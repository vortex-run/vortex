// Package api hosts VORTEX's management HTTP server. In M1.2 this is just a
// health endpoint that reports liveness and the active config hash, so a reload
// can be verified externally (e.g. curl localhost:9090/health). It is built on
// net/http from the standard library (Non-Negotiable Rule #10).
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vortex-run/vortex/internal/audit"
	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/internal/dashboard"
	proxyhttp "github.com/vortex-run/vortex/internal/proxy/http"
	"github.com/vortex-run/vortex/internal/security"
	"github.com/vortex-run/vortex/pkg/logger"
)

// DefaultAddr is the management server's default listen address.
const DefaultAddr = ":9090"

// correlationHeader is the request/response header carrying the correlation ID
// that ties together all log lines emitted while handling one request.
const correlationHeader = "X-Correlation-ID"

// Server is the management HTTP server.
type Server struct {
	srv       *http.Server
	holder    *config.Holder
	log       *slog.Logger
	version   string
	startTime time.Time

	// reloadFunc re-reads and re-validates config; set via SetReloadFunc.
	reloadFunc func() error
	// shutdownFunc triggers a graceful shutdown; set via SetShutdownFunc.
	shutdownFunc func()
	// routeStats returns per-route health; set via SetRouteStats. Kept as a
	// callback returning api-owned RouteHealth so this package need not import
	// the full proxy stack.
	routeStats func() []RouteHealth

	// auth protects management endpoints; nil means no auth (legacy/tests). keys
	// and rbac back the /api/keys endpoints. Wired via SetAuth.
	authMW func(http.Handler) http.Handler
	keys   *auth.APIKeyStore
	rbac   *auth.RBAC

	// auditLog records security-relevant API events (reload, key create/revoke).
	// nil disables audit logging (used by unit tests).
	auditLog *audit.Log

	// metricsHandler serves the Prometheus /metrics endpoint when wired.
	metricsHandler http.Handler

	// Dashboard data providers (all optional; nil yields empty/zero responses).
	// They are callbacks so this package stays decoupled from the audit, plugin,
	// and secret subsystems.
	statusProvider  func() StatusInfo
	aiCostProvider  func() AICostInfo
	healingProvider func() HealingStatus
	researchList    func() []ResearchReport
	researchGet     func(name string) (string, bool)
	devopsServers   func() []DevOpsServer
	devopsRun       func(ctx context.Context, server, command string) (string, error)
	pipelineAnalyze func(ctx context.Context, source, data, request string) (PipelineResultInfo, error)
	orchestrateRun  func(ctx context.Context, goal string) (string, error)
	secretsProvider func() []SecretStatus
	pluginsProvider func() []PluginInfo

	// Namespace management hooks (admin-only). Optional; nil yields 404/empty.
	nsLister  func() []NamespaceInfo
	nsCreator func(NamespaceInfo) error
	nsDeleter func(id string) error
	nsStats   func(id string) (NamespaceStats, bool)

	// agentRuntime backs the /api/agents endpoints. Optional; nil yields 503.
	agentRuntime AgentRuntime
	// agentLimiter rate-limits POST /api/agents/submit per source IP.
	agentLimiter *security.HTTPRateLimiter
	// M19 hardening: per-API-key budget (applied after auth), a whole-server
	// global budget, and per-IP burst auto-banning, both applied server-wide.
	keyLimiter    *security.APIKeyRateLimiter
	globalLimiter *security.GlobalRateLimiter
	burst         *security.BurstProtection
	// webhooks maps /webhook/<platform> paths to rate-limited, signature-self-
	// verifying handlers (Telegram/WhatsApp/Slack). Set via SetWebhooks.
	webhooks map[string]http.Handler
	// studioHandler serves the VORTEX Studio tree under /studio/ (auth-gated).
	studioHandler http.Handler
	// forgeRuntime backs the /api/forge endpoints. Optional; nil yields 503.
	forgeRuntime ForgeRuntime
	// logBuffer backs GET /api/logs (recent structured log lines). Optional.
	logBuffer *LogBuffer

	// readinessFunc, when set, aggregates subsystem readiness for /ready.
	readinessFunc func() error

	// trustLoopback controls whether loopback callers bypass auth on the
	// control plane. Default true (on-box `vortex reload`/`stop` work without a
	// key). Set false for deployments behind a same-host reverse proxy, where
	// every request presents a loopback RemoteAddr and the bypass would expose
	// the control plane (production audit M1). The destructive
	// /internal/shutdown endpoint never honours loopback trust regardless.
	trustLoopback bool
}

// NamespaceInfo mirrors a tenant namespace for the API.
type NamespaceInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OrgID  string `json:"org_id"`
	Quotas struct {
		MaxRoutes      int   `json:"max_routes"`
		MaxSecrets     int   `json:"max_secrets"`
		MaxConnections int64 `json:"max_connections"`
		BandwidthMbps  int64 `json:"bandwidth_mbps"`
	} `json:"quotas"`
}

// NamespaceStats is a namespace's live usage for the API.
type NamespaceStats struct {
	ActiveConns   int64 `json:"active_conns"`
	BandwidthUsed int64 `json:"bandwidth_used"`
	RouteCount    int   `json:"route_count"`
}

// SetNamespaceHooks wires the namespace management endpoints.
func (s *Server) SetNamespaceHooks(
	lister func() []NamespaceInfo,
	creator func(NamespaceInfo) error,
	deleter func(id string) error,
	stats func(id string) (NamespaceStats, bool),
) {
	s.nsLister = lister
	s.nsCreator = creator
	s.nsDeleter = deleter
	s.nsStats = stats
}

// StatusInfo is the extended status returned by GET /api/status.
type StatusInfo struct {
	NodeID          string   `json:"node_id"`
	TrustDomain     string   `json:"trust_domain"`
	TLSProvider     string   `json:"tls_provider"`
	SecretBackend   string   `json:"secret_backend"`
	PolicyDefault   bool     `json:"policy_default"`
	PluginCount     int      `json:"plugin_count"`
	AuditEntryCount int      `json:"audit_entry_count"`
	ClusterName     string   `json:"cluster_name"`
	Version         string   `json:"version"`
	WorkingDir      string   `json:"working_dir"`
	Routes          int      `json:"routes"`
	RouteNames      []string `json:"route_names,omitempty"`
}

// SecretStatus is one declared secret's set/unset state (never its value).
type SecretStatus struct {
	Name string `json:"name"`
	Set  bool   `json:"set"`
}

// PluginInfo mirrors a plugin manifest for GET /api/plugins.
type PluginInfo struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	HookTypes   []string `json:"hook_types,omitempty"`
}

// SetStatusProvider wires the GET /api/status data source.
func (s *Server) SetStatusProvider(fn func() StatusInfo) { s.statusProvider = fn }

// AICostInfo is the AI usage/cost summary returned by GET /api/ai/cost.
type AICostInfo struct {
	Provider        string  `json:"provider"`
	TotalUSD        float64 `json:"total_usd"`
	RequestsToday   int     `json:"requests_today"`
	DailyBudget     float64 `json:"daily_budget"`
	RemainingBudget float64 `json:"remaining_budget"`
	Free            bool    `json:"free"`
}

// SetAICostProvider wires the GET /api/ai/cost data source. When nil, the
// endpoint reports zero/free.
func (s *Server) SetAICostProvider(fn func() AICostInfo) { s.aiCostProvider = fn }

// StatusSnapshot returns the current extended status (for non-HTTP callers like
// the Telegram bot). Empty when no provider is wired.
func (s *Server) StatusSnapshot() StatusInfo {
	if s.statusProvider == nil {
		return StatusInfo{}
	}
	return s.statusProvider()
}

// AICostSnapshot returns the current AI cost summary (free when unwired).
func (s *Server) AICostSnapshot() AICostInfo {
	if s.aiCostProvider == nil {
		return AICostInfo{Free: true}
	}
	return s.aiCostProvider()
}

// handleAICost returns today's AI usage and budget.
func (s *Server) handleAICost(w http.ResponseWriter, _ *http.Request) {
	info := AICostInfo{Free: true}
	if s.aiCostProvider != nil {
		info = s.aiCostProvider()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

// SetSecretsProvider wires the GET /api/secrets/status data source.
func (s *Server) SetSecretsProvider(fn func() []SecretStatus) { s.secretsProvider = fn }

// SetPluginsProvider wires the GET /api/plugins data source.
func (s *Server) SetPluginsProvider(fn func() []PluginInfo) { s.pluginsProvider = fn }

// SetMetricsHandler wires the Prometheus metrics handler served at /metrics.
func (s *Server) SetMetricsHandler(h http.Handler) { s.metricsHandler = h }

// SetAuditLog wires the audit log used to record reload and key-management
// events. A nil log disables audit recording.
func (s *Server) SetAuditLog(l *audit.Log) { s.auditLog = l }

// audit records an event if an audit log is wired; it is a no-op otherwise.
func (s *Server) audit(actor, action, resource string, detail map[string]any) {
	if s.auditLog == nil {
		return
	}
	if err := s.auditLog.Append(context.Background(), actor, action, resource, detail); err != nil {
		s.log.Warn("audit append failed", "action", action, "err", err)
	}
}

// SetAuth wires the authentication middleware and the key/role stores backing
// the /api/keys endpoints. It must be called before New builds the mux — so it
// is passed through New via the optional AuthDeps rather than set afterward.
// (Provided as a method for symmetry with the other Set* wirings used in tests
// that construct the server in stages.)
func (s *Server) SetAuth(mw func(http.Handler) http.Handler, keys *auth.APIKeyStore, rbac *auth.RBAC) {
	s.authMW = mw
	s.keys = keys
	s.rbac = rbac
}

// RouteHealth is one route's health summary in the /health response.
type RouteHealth struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Listen   string `json:"listen"`
	Active   int64  `json:"active"`
}

// SetRouteStats registers a callback supplying live per-route stats for the
// /health response. start.go wires this to the proxy manager after it starts.
func (s *Server) SetRouteStats(fn func() []RouteHealth) { s.routeStats = fn }

// SetReloadFunc registers the callback invoked by POST /internal/reload. It
// should re-read and re-validate the config and return an error if invalid.
func (s *Server) SetReloadFunc(fn func() error) { s.reloadFunc = fn }

// SetShutdownFunc registers the callback invoked by POST /internal/shutdown to
// begin a graceful shutdown.
func (s *Server) SetShutdownFunc(fn func()) { s.shutdownFunc = fn }

// New constructs a management Server. holder supplies the live config so
// /health always reports the currently active hash, including after a reload.
func New(addr string, holder *config.Holder, version string, log *slog.Logger) *Server {
	if addr == "" {
		addr = DefaultAddr
	}
	s := &Server{
		holder:    holder,
		log:       log,
		version:   version,
		startTime: time.Now(),
		// Agent submit is expensive (spawns work, may call an AI provider), so
		// it is rate-limited per source IP: 10/min, burst 5.
		agentLimiter: security.NewHTTPRateLimiter(security.HTTPRateLimiterConfig{
			RPM: 10, Burst: 5, Enabled: true,
		}),
		keyLimiter:    security.NewAPIKeyRateLimiter(security.APIKeyRateLimiterConfig{Enabled: true}),
		globalLimiter: security.NewGlobalRateLimiter(0),
		burst:         security.NewBurstProtection(security.BurstProtectionConfig{}),
		// Loopback trust defaults on; operators behind a same-host proxy set
		// VORTEX_TRUST_LOOPBACK=false so a loopback RemoteAddr does not bypass
		// control-plane auth (production audit M1).
		trustLoopback: loopbackTrustEnabled(),
	}
	mux := http.NewServeMux()
	// Public endpoints — liveness/readiness must never require auth.
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	// The management dashboard SPA is served (unauthenticated; it is the login
	// surface) under /dashboard/.
	mux.Handle("GET /dashboard/", dashboard.Handler())
	// Protected endpoints — wrapped at request time with the auth middleware
	// (if one has been wired via SetAuth) so a missing/invalid credential is
	// rejected before the handler runs.
	mux.Handle("POST /internal/reload", s.protected(http.HandlerFunc(s.handleInternalReload)))
	// /internal/shutdown is the most destructive endpoint: it never honours
	// loopback trust, so a co-located/sidecar process cannot shut the server
	// down without a credential (production audit M1). When no auth is wired
	// (tests) it still runs — there is no key to require.
	mux.Handle("POST /internal/shutdown", s.protectedStrict(http.HandlerFunc(s.handleInternalShutdown)))
	// /metrics is protected like the control plane: reachable from localhost
	// (scrapers commonly run on-box) or with a valid key.
	mux.Handle("GET /metrics", s.protected(http.HandlerFunc(s.handleMetrics)))
	mux.Handle("GET /api/keys", s.protectedAdmin(http.HandlerFunc(s.handleListKeys)))
	mux.Handle("POST /api/keys", s.protectedAdmin(http.HandlerFunc(s.handleCreateKey)))
	mux.Handle("DELETE /api/keys/{id}", s.protectedAdmin(http.HandlerFunc(s.handleRevokeKey)))

	// Dashboard data endpoints (protected: localhost or valid key).
	mux.Handle("GET /api/status", s.protected(http.HandlerFunc(s.handleStatus)))
	mux.Handle("GET /api/ai/cost", s.requireAPIKey(http.HandlerFunc(s.handleAICost)))
	mux.Handle("GET /api/healing/status", s.requireAPIKey(http.HandlerFunc(s.handleHealingStatus)))
	mux.Handle("GET /api/healing/events", s.requireAPIKey(http.HandlerFunc(s.handleHealingEvents)))
	mux.Handle("GET /api/research/reports", s.requireAPIKey(http.HandlerFunc(s.handleResearchReports)))
	mux.Handle("GET /api/research/reports/{name}", s.requireAPIKey(http.HandlerFunc(s.handleResearchReport)))
	mux.Handle("GET /api/devops/servers", s.requireAPIKey(http.HandlerFunc(s.handleDevOpsServers)))
	mux.Handle("POST /api/devops/command", s.requireAPIKey(http.HandlerFunc(s.handleDevOpsCommand)))
	mux.Handle("POST /api/pipeline/analyze", s.requireAPIKey(http.HandlerFunc(s.handlePipelineAnalyze)))
	mux.Handle("POST /api/orchestrate", s.requireAPIKey(http.HandlerFunc(s.handleOrchestrate)))
	mux.Handle("GET /api/secrets/status", s.protected(http.HandlerFunc(s.handleSecretsStatus)))
	mux.Handle("GET /api/plugins", s.protected(http.HandlerFunc(s.handlePlugins)))
	mux.Handle("GET /api/audit", s.protected(http.HandlerFunc(s.handleAudit)))
	mux.Handle("POST /api/audit/verify", s.protected(http.HandlerFunc(s.handleAuditVerify)))

	// Namespace management (admin only).
	// Agent runtime (M10).
	// Agent endpoints are the data plane (they can run tools), so unlike the
	// /internal/* control plane they require a real API key even from localhost
	// (no loopback bypass). Submit is additionally rate-limited per IP.
	mux.Handle("POST /api/agents/submit",
		s.requireAPIKey(s.rateLimitAgents(http.HandlerFunc(s.handleAgentSubmit))))
	mux.Handle("GET /api/agents/status", s.requireAPIKey(http.HandlerFunc(s.handleAgentStatus)))
	mux.Handle("POST /api/agents/approve", s.requireAPIKey(http.HandlerFunc(s.handleAgentApprove)))
	mux.Handle("GET /api/agents/history", s.requireAPIKey(http.HandlerFunc(s.handleAgentHistory)))
	mux.Handle("GET /api/agents/history/{id}", s.requireAPIKey(http.HandlerFunc(s.handleAgentSessionHistory)))

	// VORTEX Forge (M13) — autonomous app builder. Data-plane: requires a key.
	mux.Handle("POST /api/forge/build", s.requireAPIKey(http.HandlerFunc(s.handleForgeBuild)))
	mux.Handle("GET /api/forge/status/{id}", s.requireAPIKey(http.HandlerFunc(s.handleForgeStatus)))
	mux.Handle("GET /api/forge/jobs", s.requireAPIKey(http.HandlerFunc(s.handleForgeJobs)))

	// Recent structured logs (for the TUI log viewer), auth-gated.
	mux.Handle("GET /api/logs", s.requireAPIKey(http.HandlerFunc(s.handleLogs)))

	// Messaging webhooks (M11): no API-key auth (each verifies its own platform
	// signature), per-IP rate limited. Both GET (WhatsApp verification) and POST
	// route through the same dispatcher.
	mux.HandleFunc("/webhook/", s.handleWebhook)

	// VORTEX Studio (M12): browser IDE/terminal/db/git, auth-gated.
	mux.HandleFunc("/studio/", s.handleStudio)

	mux.Handle("GET /api/namespaces", s.protectedAdmin(http.HandlerFunc(s.handleListNamespaces)))
	mux.Handle("POST /api/namespaces", s.protectedAdmin(http.HandlerFunc(s.handleCreateNamespace)))
	mux.Handle("DELETE /api/namespaces/{id}", s.protectedAdmin(http.HandlerFunc(s.handleDeleteNamespace)))
	mux.Handle("GET /api/namespaces/{id}/stats", s.protectedAdmin(http.HandlerFunc(s.handleNamespaceStats)))

	// Server-level protection (M19), outermost-in: security headers,
	// correlation IDs, per-IP burst auto-banning, then the global request
	// budget. Per-key limits are applied per-route after auth (see keyLimit).
	handler := s.globalLimiter.Middleware()(mux)
	handler = s.burst.Middleware()(handler)
	handler = s.correlationMiddleware(handler)
	handler = proxyhttp.NewSecurityHeaders(proxyhttp.SecurityHeadersConfig{Version: version})(handler)
	s.srv = &http.Server{
		Addr:    addr,
		Handler: handler,
		// Bound every phase of a request so slowloris-style and idle
		// connections cannot pin resources indefinitely (production audit I4).
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// SetReadinessFunc registers a callback that reports whether the server's
// subsystems are ready. When set, /ready returns 503 if it returns a non-nil
// error, instead of always reporting ready (production audit I3). The error
// message is surfaced in the response body.
func (s *Server) SetReadinessFunc(fn func() error) { s.readinessFunc = fn }

// SetBurstNotifier wires the out-of-band alert (e.g. Telegram via the
// notification router) sent when burst protection bans an IP.
func (s *Server) SetBurstNotifier(fn func(title, body string)) {
	s.burst.SetNotify(fn)
}

// keyLimit applies the per-API-key rate limiter to an authenticated request.
// It runs inside the auth middleware so the authenticated user is in context;
// unauthenticated requests (e.g. loopback bypass) pass through and rely on
// the per-IP and global limiters instead. Admin keys are never limited.
func (s *Server) keyLimit(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.UserFromContext(r.Context())
		if ok && s.keyLimiter != nil {
			admin := false
			for _, role := range user.Roles {
				if role == auth.RoleAdmin {
					admin = true
					break
				}
			}
			if !s.keyLimiter.Allow(user.ID, admin) {
				s.log.Warn("API key rate limit exceeded", "user", user.ID, "path", r.URL.Path)
				w.Header().Set("Retry-After", "60")
				s.writeJSON(w, http.StatusTooManyRequests, map[string]string{
					"error": "API key rate limit exceeded", "retry_after": "60",
				})
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

// protected wraps h with the auth middleware (if wired) — except for loopback
// requests, which are allowed through without a credential WHEN loopback trust
// is enabled (the default). The /internal/* control-plane endpoints stand in
// for SIGHUP/SIGTERM used by `vortex reload`/`stop`, so an on-box call is
// implicitly trusted. Operators behind a same-host reverse proxy disable the
// bypass with VORTEX_TRUST_LOOPBACK=false (production audit M1). With no auth
// wired the handler runs unchanged.
func (s *Server) protected(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authMW == nil || (s.trustLoopback && localhostOnly(r)) {
			h.ServeHTTP(w, r)
			return
		}
		s.authMW(s.keyLimit(h)).ServeHTTP(w, r)
	})
}

// protectedStrict is like protected but never grants the loopback bypass: a
// valid credential is always required when auth is wired. Used for the
// destructive /internal/shutdown endpoint.
func (s *Server) protectedStrict(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authMW == nil {
			h.ServeHTTP(w, r)
			return
		}
		s.authMW(s.keyLimit(h)).ServeHTTP(w, r)
	})
}

// loopbackTrustEnabled reports whether loopback callers bypass control-plane
// auth. Enabled by default; set VORTEX_TRUST_LOOPBACK=false/0/no to disable.
func loopbackTrustEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VORTEX_TRUST_LOOPBACK"))) {
	case "false", "0", "no", "off":
		return false
	default:
		return true
	}
}

// protectedAdmin is like protected but additionally requires the admin role,
// stamped into the context so the auth middleware enforces it.
func (s *Server) protectedAdmin(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authMW == nil {
			h.ServeHTTP(w, r)
			return
		}
		r = r.WithContext(auth.SetRequiredRole(r.Context(), auth.RoleAdmin))
		s.authMW(s.keyLimit(h)).ServeHTTP(w, r)
	})
}

// correlationMiddleware ensures every request carries a correlation ID. It
// reuses an incoming X-Correlation-ID header when present (so a caller's ID
// flows through the system) or generates a fresh 32-char hex ID otherwise. The
// ID is echoed back in the response header — set before the wrapped handler can
// write anything — and stored in the request context so downstream logging
// (via pkg/logger) automatically tags log lines with it.
func (s *Server) correlationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(correlationHeader)
		if id == "" {
			id = newCorrelationID()
		}
		// Must be set before the handler writes the body or status.
		w.Header().Set(correlationHeader, id)
		ctx := logger.WithCorrelationID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// newCorrelationID returns a 32-character hex string (16 random bytes). If the
// system RNG fails (effectively never), it falls back to a timestamp-derived
// value so a request is never left without an ID.
func newCorrelationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405.000000")))[:32]
	}
	return hex.EncodeToString(b[:])
}

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.srv.Addr }

// healthResponse is the JSON body returned by /health. ClusterName is omitted
// for anonymous callers — a deployment identifier should not leak on an
// unauthenticated endpoint (production audit C1) — and included only for
// authenticated/loopback requests.
type healthResponse struct {
	Status      string        `json:"status"`
	Version     string        `json:"version"`
	ConfigHash  string        `json:"config_hash"`
	ClusterName string        `json:"cluster_name,omitempty"`
	Uptime      string        `json:"uptime"`
	Routes      []RouteHealth `json:"routes,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	cfg := s.holder.Get()
	resp := healthResponse{
		Status:     "ok",
		Version:    s.version,
		ConfigHash: cfg.Hash(),
		Uptime:     time.Since(s.startTime).Round(time.Second).String(),
	}
	// Only reveal the cluster name to trusted callers. Loopback peers are
	// trusted like the rest of the control plane; remote authenticated callers
	// read it from /api/status instead. When no auth is wired (tests/legacy)
	// the field is populated as before so existing behaviour is preserved.
	if s.authMW == nil || (s.trustLoopback && localhostOnly(r)) {
		resp.ClusterName = cfg.Cluster.Name
	}
	if s.routeStats != nil {
		resp.Routes = s.routeStats()
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.Error("encoding health response", "err", err)
	}
}

// handleMetrics serves the Prometheus metrics exposition when a handler is
// wired; otherwise it reports that metrics are unconfigured.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metricsHandler == nil {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}
	s.metricsHandler.ServeHTTP(w, r)
}

// handleReady is a readiness probe. When a readiness func is registered
// (SetReadinessFunc) it aggregates subsystem health and returns 503 with the
// reason if anything is not ready (production audit I3); otherwise it reports
// ready, since reaching this handler means boot completed.
func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.readinessFunc != nil {
		if err := s.readinessFunc(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"ready": false, "reason": err.Error()})
			return
		}
	}
	if err := json.NewEncoder(w).Encode(map[string]bool{"ready": true}); err != nil {
		s.log.Error("encoding ready response", "err", err)
	}
}

// Start begins serving in a background goroutine. It returns immediately; serve
// errors other than graceful shutdown are logged.
func (s *Server) Start() {
	go func() {
		s.log.Info("management API listening", "addr", s.srv.Addr)
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Error("management API stopped unexpectedly", "err", err)
		}
	}()
}

// StartCleanup runs the background sweeps that bound the rate-limiter maps
// (per-IP agent limiter, per-key limiter, burst-protection windows/bans) so
// none grows without bound under a churn of distinct IPs/keys (production
// audit H5). It returns when ctx is cancelled.
func (s *Server) StartCleanup(ctx context.Context) {
	go s.agentLimiter.StartCleanup(ctx)
	go s.keyLimiter.StartCleanup(ctx)
	go s.burst.StartCleanup(ctx)
}

// Shutdown gracefully stops the server, respecting ctx's deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
