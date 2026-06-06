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
	"time"

	"github.com/vortex-run/vortex/internal/audit"
	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/internal/dashboard"
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
	secretsProvider func() []SecretStatus
	pluginsProvider func() []PluginInfo

	// Namespace management hooks (admin-only). Optional; nil yields 404/empty.
	nsLister  func() []NamespaceInfo
	nsCreator func(NamespaceInfo) error
	nsDeleter func(id string) error
	nsStats   func(id string) (NamespaceStats, bool)
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
	NodeID          string `json:"node_id"`
	TrustDomain     string `json:"trust_domain"`
	TLSProvider     string `json:"tls_provider"`
	SecretBackend   string `json:"secret_backend"`
	PolicyDefault   bool   `json:"policy_default"`
	PluginCount     int    `json:"plugin_count"`
	AuditEntryCount int    `json:"audit_entry_count"`
	ClusterName     string `json:"cluster_name"`
	Version         string `json:"version"`
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
	mux.Handle("POST /internal/shutdown", s.protected(http.HandlerFunc(s.handleInternalShutdown)))
	// /metrics is protected like the control plane: reachable from localhost
	// (scrapers commonly run on-box) or with a valid key.
	mux.Handle("GET /metrics", s.protected(http.HandlerFunc(s.handleMetrics)))
	mux.Handle("GET /api/keys", s.protectedAdmin(http.HandlerFunc(s.handleListKeys)))
	mux.Handle("POST /api/keys", s.protectedAdmin(http.HandlerFunc(s.handleCreateKey)))
	mux.Handle("DELETE /api/keys/{id}", s.protectedAdmin(http.HandlerFunc(s.handleRevokeKey)))

	// Dashboard data endpoints (protected: localhost or valid key).
	mux.Handle("GET /api/status", s.protected(http.HandlerFunc(s.handleStatus)))
	mux.Handle("GET /api/secrets/status", s.protected(http.HandlerFunc(s.handleSecretsStatus)))
	mux.Handle("GET /api/plugins", s.protected(http.HandlerFunc(s.handlePlugins)))
	mux.Handle("GET /api/audit", s.protected(http.HandlerFunc(s.handleAudit)))
	mux.Handle("POST /api/audit/verify", s.protected(http.HandlerFunc(s.handleAuditVerify)))

	// Namespace management (admin only).
	mux.Handle("GET /api/namespaces", s.protectedAdmin(http.HandlerFunc(s.handleListNamespaces)))
	mux.Handle("POST /api/namespaces", s.protectedAdmin(http.HandlerFunc(s.handleCreateNamespace)))
	mux.Handle("DELETE /api/namespaces/{id}", s.protectedAdmin(http.HandlerFunc(s.handleDeleteNamespace)))
	mux.Handle("GET /api/namespaces/{id}/stats", s.protectedAdmin(http.HandlerFunc(s.handleNamespaceStats)))

	s.srv = &http.Server{
		Addr:              addr,
		Handler:           s.correlationMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// protected wraps h with the auth middleware (if wired) — except for loopback
// requests, which are allowed through without a credential. The /internal/*
// control-plane endpoints are already restricted to localhost (the Windows-safe
// SIGHUP/SIGTERM equivalents used by `vortex reload`/`stop`), so an on-box call
// is implicitly trusted; a remote call still needs a valid key. With no auth
// wired the handler runs unchanged.
func (s *Server) protected(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authMW == nil || localhostOnly(r) {
			h.ServeHTTP(w, r)
			return
		}
		s.authMW(h).ServeHTTP(w, r)
	})
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
		s.authMW(h).ServeHTTP(w, r)
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

// healthResponse is the JSON body returned by /health.
type healthResponse struct {
	Status      string        `json:"status"`
	Version     string        `json:"version"`
	ConfigHash  string        `json:"config_hash"`
	ClusterName string        `json:"cluster_name"`
	Uptime      string        `json:"uptime"`
	Routes      []RouteHealth `json:"routes,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	cfg := s.holder.Get()
	resp := healthResponse{
		Status:      "ok",
		Version:     s.version,
		ConfigHash:  cfg.Hash(),
		ClusterName: cfg.Cluster.Name,
		Uptime:      time.Since(s.startTime).Round(time.Second).String(),
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

// handleReady is a readiness probe. The management server only begins serving
// once boot has completed, so reaching this handler means VORTEX is ready; it
// returns 200 with a small JSON body. (As subsystems gain their own readiness
// gates in later milestones, this will aggregate them.)
func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
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

// Shutdown gracefully stops the server, respecting ctx's deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
