// Package proxy wires VORTEX's config into running data-plane listeners (the
// end-of-M2 integration step): for each configured route it starts the matching
// TCP tunnel, UDP tunnel, HTTP/HTTPS reverse proxy, or QUIC/HTTP3 dual-stack
// listener, runs them under one lifecycle, and aggregates their stats.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/vortex-run/vortex/internal/config"
	proxygateway "github.com/vortex-run/vortex/internal/proxy/gateway"
	proxyhttp "github.com/vortex-run/vortex/internal/proxy/http"
	proxyquic "github.com/vortex-run/vortex/internal/proxy/quic"
	"github.com/vortex-run/vortex/internal/proxy/tcp"
	proxyudp "github.com/vortex-run/vortex/internal/proxy/udp"
	vtls "github.com/vortex-run/vortex/internal/tls"
)

// stopTimeout bounds how long Stop waits for all listeners to finish.
const stopTimeout = 30 * time.Second

// ManagerConfig configures a proxy Manager.
type ManagerConfig struct {
	// Config is the validated VORTEX configuration. Required.
	Config *config.Config
	// TLS supplies certificates for https/h3 routes. May be nil when no route
	// needs TLS.
	TLS *vtls.Manager
	// TCPPool is shared by all routes for backend dialing. Required.
	TCPPool *tcp.Pool
	// MTLSConfig provides the mTLS server config for routes with mtls:true. May
	// be nil when no route uses mTLS.
	MTLSConfig *vtls.MTLSConfig
	// Logger receives route lifecycle events; defaults to slog.Default.
	Logger *slog.Logger
}

// RouteStats is a per-route runtime snapshot.
type RouteStats struct {
	Name     string
	Protocol string
	Listen   string
	Active   int64
	Total    int64
	Backends int
}

// route is one configured route plus its live listener.
type route struct {
	cfg      config.Route
	protocol string
	listen   string
	backends int
	run      func(context.Context) error
	stats    func() (active, total int64)
}

// Manager owns all data-plane listeners derived from the config.
type Manager struct {
	cfg    ManagerConfig
	log    *slog.Logger
	routes []*route

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewManager validates cfg and builds (but does not start) a listener for each
// route. It returns an error if the config is missing, a required dependency is
// absent, or any route has an unknown protocol or fails to initialise.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.Config == nil {
		return nil, errors.New("proxy: Config is required")
	}
	if cfg.TCPPool == nil {
		return nil, errors.New("proxy: TCPPool is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	m := &Manager{cfg: cfg, log: cfg.Logger}
	for _, rc := range cfg.Config.Routes {
		r, err := m.buildRoute(rc)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", rc.Name, err)
		}
		m.routes = append(m.routes, r)
	}
	return m, nil
}

// buildRoute constructs the listener closure and stats accessor for one route.
func (m *Manager) buildRoute(rc config.Route) (*route, error) {
	switch rc.Protocol {
	case "tcp":
		return m.buildTCP(rc)
	case "udp":
		return m.buildUDP(rc)
	case "http":
		return m.buildHTTP(rc, false)
	case "https":
		return m.buildHTTP(rc, true)
	case "h3":
		return m.buildH3(rc)
	default:
		return nil, fmt.Errorf("unknown protocol %q", rc.Protocol)
	}
}

func (m *Manager) buildTCP(rc config.Route) (*route, error) {
	lc := tcp.ListenerConfig{
		ListenAddr:     listenAddr(rc.Listen),
		Backends:       routeToTCPBackends(rc.Backends),
		Pool:           m.cfg.TCPPool,
		MaxConnections: 0,
		Logger:         m.log,
	}
	// mTLS routes require the cluster identity mesh; wrap accepted connections
	// in the mTLS server config so peers must present a valid cluster cert.
	if rc.MTLS {
		if m.cfg.MTLSConfig == nil {
			return nil, errors.New("route has mtls:true but no mTLS config was provided")
		}
		lc.TLSConfig = m.cfg.MTLSConfig.ServerTLSConfig()
	}
	ln, err := tcp.NewListener(lc)
	if err != nil {
		return nil, err
	}
	return &route{
		cfg: rc, protocol: "tcp", listen: listenAddr(rc.Listen), backends: len(rc.Backends),
		run: ln.Listen,
		stats: func() (int64, int64) {
			s := ln.Stats()
			return s.Active, s.Total
		},
	}, nil
}

func (m *Manager) buildUDP(rc config.Route) (*route, error) {
	if len(rc.Backends) == 0 {
		return nil, errors.New("udp route requires a backend")
	}
	ln, err := proxyudp.NewListener(proxyudp.UDPListenerConfig{
		ListenAddr:  listenAddr(rc.Listen),
		BackendAddr: backendHostPort(rc.Backends[0]),
		RateLimit:   rateLimitRPS(rc.RateLimit),
		Logger:      m.log,
	})
	if err != nil {
		return nil, err
	}
	return &route{
		cfg: rc, protocol: "udp", listen: listenAddr(rc.Listen), backends: len(rc.Backends),
		run: ln.Listen,
		stats: func() (int64, int64) {
			s := ln.Stats()
			return int64(s.Sessions.Active), s.Sessions.Total
		},
	}, nil
}

func (m *Manager) buildHTTP(rc config.Route, useTLS bool) (*route, error) {
	handler, err := proxyhttp.NewHandler(proxyhttp.HandlerConfig{
		Backends:     routeToHTTPBackends(rc.Backends),
		Balancer:     "round-robin",
		RoundTripper: proxyhttp.NewRoundTripper(proxyhttp.RoundTripperConfig{Pool: m.cfg.TCPPool}),
		Timeout:      parseTimeout(rc.Timeout),
	})
	if err != nil {
		return nil, err
	}

	// Wrap with the protocol gateway so WebSocket/gRPC on this route work.
	gw, err := proxygateway.NewGateway(proxygateway.GatewayConfig{
		HTTPHandler: handler,
		TCPPool:     m.cfg.TCPPool,
		Sticky:      proxygateway.NewStickySession(),
		WSBackends:  routeToHTTPBackends(rc.Backends),
	})
	if err != nil {
		return nil, err
	}

	router := proxyhttp.NewRouter()
	router.Handle(routePattern(rc), gw)

	srvCfg := proxyhttp.ServerConfig{Addr: listenAddr(rc.Listen), Router: router}
	if useTLS {
		if m.cfg.TLS == nil {
			return nil, errors.New("https route requires a TLS manager")
		}
		srvCfg.TLSConfig = m.cfg.TLS.TLSConfig()
	}
	srv := proxyhttp.NewServer(srvCfg)

	proto := "http"
	if useTLS {
		proto = "https"
	}
	return &route{
		cfg: rc, protocol: proto, listen: srvCfg.Addr, backends: len(rc.Backends),
		run: srv.ListenAndServe,
		stats: func() (int64, int64) {
			s := srv.Stats()
			return s.ActiveConns, s.TotalReqs
		},
	}, nil
}

func (m *Manager) buildH3(rc config.Route) (*route, error) {
	if m.cfg.TLS == nil {
		return nil, errors.New("h3 route requires a TLS manager")
	}
	handler, err := proxyhttp.NewHandler(proxyhttp.HandlerConfig{
		Backends:     routeToHTTPBackends(rc.Backends),
		Balancer:     "round-robin",
		RoundTripper: proxyhttp.NewRoundTripper(proxyhttp.RoundTripperConfig{Pool: m.cfg.TCPPool}),
		Timeout:      parseTimeout(rc.Timeout),
	})
	if err != nil {
		return nil, err
	}
	router := proxyhttp.NewRouter()
	router.Handle(routePattern(rc), handler)

	ds, err := proxyquic.NewDualStack(proxyquic.DualStackConfig{
		Addr:      listenAddr(rc.Listen),
		TLSConfig: m.cfg.TLS.TLSConfig(),
		Router:    router,
		Logger:    m.log,
	})
	if err != nil {
		return nil, err
	}
	return &route{
		cfg: rc, protocol: "h3", listen: listenAddr(rc.Listen), backends: len(rc.Backends),
		run: ds.ListenAndServe,
		stats: func() (int64, int64) {
			s := ds.Stats()
			return s.TCP.ActiveConns, s.TCP.TotalReqs
		},
	}, nil
}

// Start runs every route's listener concurrently and blocks until ctx is
// cancelled (returns nil) or a listener fails fatally (cancels the rest and
// returns the error).
func (m *Manager) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	defer cancel()

	errCh := make(chan error, len(m.routes))
	for _, r := range m.routes {
		r := r
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.log.Info("route started",
				"name", r.cfg.Name, "protocol", r.protocol,
				"listen", r.listen, "backends", r.backends)
			if err := r.run(ctx); err != nil && ctx.Err() == nil {
				errCh <- fmt.Errorf("route %q: %w", r.cfg.Name, err)
			}
			m.log.Info("route stopped", "name", r.cfg.Name, "protocol", r.protocol)
		}()
	}

	select {
	case <-ctx.Done():
		m.wg.Wait()
		return nil
	case err := <-errCh:
		cancel()
		m.wg.Wait()
		return err
	}
}

// Stop cancels all listeners and waits up to stopTimeout for them to finish.
func (m *Manager) Stop(_ context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(stopTimeout):
		return fmt.Errorf("proxy: %d listener(s) did not stop within %s", len(m.routes), stopTimeout)
	}
}

// Stats returns one RouteStats per configured route.
func (m *Manager) Stats() []RouteStats {
	out := make([]RouteStats, 0, len(m.routes))
	for _, r := range m.routes {
		active, total := r.stats()
		out = append(out, RouteStats{
			Name:     r.cfg.Name,
			Protocol: r.protocol,
			Listen:   r.listen,
			Active:   active,
			Total:    total,
			Backends: r.backends,
		})
	}
	return out
}

// --- conversion helpers ----------------------------------------------------

// listenAddr returns ":<port>" for an L4 route, or ":0" if unset (L7 routes
// bind their own address; this is the fallback bind).
func listenAddr(port int) string {
	if port <= 0 {
		return ":0"
	}
	return ":" + strconv.Itoa(port)
}

func backendHostPort(b config.Backend) string {
	return b.Host + ":" + strconv.Itoa(b.Port)
}

// routeToTCPBackends converts config backends to tcp.BackendAddr.
func routeToTCPBackends(backends []config.Backend) []tcp.BackendAddr {
	out := make([]tcp.BackendAddr, len(backends))
	for i, b := range backends {
		out[i] = tcp.BackendAddr{Addr: backendHostPort(b), Weight: b.Weight}
	}
	return out
}

// routeToHTTPBackends converts config backends to proxyhttp.BackendAddr.
func routeToHTTPBackends(backends []config.Backend) []proxyhttp.BackendAddr {
	out := make([]proxyhttp.BackendAddr, len(backends))
	for i, b := range backends {
		out[i] = proxyhttp.BackendAddr{Addr: backendHostPort(b), Weight: b.Weight}
	}
	return out
}

// routePattern returns the router pattern for an L7 route: host + "/*" so all
// paths under the host match, or "/*" (any host) when no host is set.
func routePattern(rc config.Route) string {
	if rc.Host != "" {
		return rc.Host + "/*"
	}
	return "/*"
}

// parseTimeout parses a Go duration string; invalid/empty yields 0 (handler default).
func parseTimeout(s string) time.Duration {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

// rateLimitRPS converts a route's RPM rate limit to packets/sec for UDP; 0 (no
// limit) when unset.
func rateLimitRPS(rl *config.RateLimit) int {
	if rl == nil || rl.RPM <= 0 {
		return 0
	}
	if rps := rl.RPM / 60; rps > 0 {
		return rps
	}
	return 1
}
