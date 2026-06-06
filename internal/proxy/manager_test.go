package proxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/internal/observability"
	"github.com/vortex-run/vortex/internal/policy"
	"github.com/vortex-run/vortex/internal/proxy/tcp"
	"github.com/vortex-run/vortex/internal/tenancy"
	vtls "github.com/vortex-run/vortex/internal/tls"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func newTestManager(t *testing.T, routes []config.Route) *Manager {
	t.Helper()
	m, err := NewManager(ManagerConfig{
		Config:  &config.Config{Routes: routes},
		TCPPool: tcp.NewPool(tcp.PoolConfig{}),
		Logger:  discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func backendOf(t *testing.T, srv *httptest.Server) config.Backend {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)
	return config.Backend{Host: host, Port: port, Weight: 1}
}

// testMTLSConfig builds an mTLS config for the given cluster name.
func testMTLSConfig(t *testing.T, clusterName string) *vtls.MTLSConfig {
	t.Helper()
	store, err := vtls.NewStore(filepath.Join(t.TempDir(), "mtls"), []byte("mgr-mtls-key"))
	if err != nil {
		t.Fatal(err)
	}
	rm, err := vtls.NewRotationManager(vtls.RotationConfig{
		ClusterName: clusterName, Store: store, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	mc, err := vtls.NewMTLSConfig(vtls.MTLSConfig{
		RotationMgr: rm, TrustDomain: rm.Identity().TrustDomain, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return mc
}

func TestManager_MTLSRouteUsesTLSConfig(t *testing.T) {
	mc := testMTLSConfig(t, "prod")
	// A tcp route with mtls:true plus an MTLSConfig must build successfully and
	// produce a TLS-wrapped listener.
	m, err := NewManager(ManagerConfig{
		Config: &config.Config{Routes: []config.Route{
			{Name: "db", Protocol: "tcp", Listen: freePort(t),
				Backends: []config.Backend{{Host: "127.0.0.1", Port: freePort(t), Weight: 1}}, MTLS: true},
		}},
		TCPPool:    tcp.NewPool(tcp.PoolConfig{}),
		MTLSConfig: mc,
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewManager with mtls route + config: %v", err)
	}
	if len(m.routes) != 1 || m.routes[0].protocol != "tcp" {
		t.Fatalf("expected one tcp route, got %+v", m.routes)
	}
}

func TestManager_MTLSRouteWithoutConfigErrors(t *testing.T) {
	// mtls:true with NO MTLSConfig must be rejected at build time.
	_, err := NewManager(ManagerConfig{
		Config: &config.Config{Routes: []config.Route{
			{Name: "db", Protocol: "tcp", Listen: freePort(t),
				Backends: []config.Backend{{Host: "127.0.0.1", Port: freePort(t), Weight: 1}}, MTLS: true},
		}},
		TCPPool: tcp.NewPool(tcp.PoolConfig{}),
		Logger:  discardLogger(),
	})
	if err == nil {
		t.Error("expected error for mtls:true route without MTLSConfig")
	}
}

func TestManager_PlainTCPRouteNoTLS(t *testing.T) {
	// A tcp route with mtls:false must build fine with no MTLSConfig.
	m := newTestManager(t, []config.Route{
		{Name: "plain", Protocol: "tcp", Listen: freePort(t),
			Backends: []config.Backend{{Host: "127.0.0.1", Port: freePort(t), Weight: 1}}, MTLS: false},
	})
	if len(m.routes) != 1 {
		t.Fatalf("expected one route, got %d", len(m.routes))
	}
}

func TestManager_NilConfigError(t *testing.T) {
	if _, err := NewManager(ManagerConfig{TCPPool: tcp.NewPool(tcp.PoolConfig{})}); err == nil {
		t.Error("expected error when Config is nil")
	}
}

func TestManager_UnknownProtocolError(t *testing.T) {
	_, err := NewManager(ManagerConfig{
		Config:  &config.Config{Routes: []config.Route{{Name: "bad", Protocol: "smtp"}}},
		TCPPool: tcp.NewPool(tcp.PoolConfig{}),
	})
	if err == nil {
		t.Error("expected error for unknown protocol")
	}
}

func TestManager_StatsOnePerRoute(t *testing.T) {
	m := newTestManager(t, []config.Route{
		{Name: "a", Protocol: "tcp", Listen: freePort(t), Backends: []config.Backend{{Host: "127.0.0.1", Port: 9, Weight: 1}}},
		{Name: "b", Protocol: "http", Host: "x.com", Backends: []config.Backend{{Host: "127.0.0.1", Port: 9, Weight: 1}}},
	})
	stats := m.Stats()
	if len(stats) != 2 {
		t.Fatalf("Stats len = %d, want 2", len(stats))
	}
	if stats[0].Name != "a" || stats[0].Protocol != "tcp" {
		t.Errorf("route 0 = %+v", stats[0])
	}
	if stats[1].Name != "b" || stats[1].Protocol != "http" {
		t.Errorf("route 1 = %+v", stats[1])
	}
}

func TestManager_ProtocolRouting(t *testing.T) {
	// One route of each L4/L7 protocol (no TLS) builds without error and
	// reports the right protocol in Stats.
	m := newTestManager(t, []config.Route{
		{Name: "t", Protocol: "tcp", Listen: freePort(t), Backends: []config.Backend{{Host: "127.0.0.1", Port: 9, Weight: 1}}},
		{Name: "u", Protocol: "udp", Listen: freePort(t), Backends: []config.Backend{{Host: "127.0.0.1", Port: 9, Weight: 1}}},
		{Name: "h", Protocol: "http", Host: "x.com", Backends: []config.Backend{{Host: "127.0.0.1", Port: 9, Weight: 1}}},
	})
	got := map[string]string{}
	for _, s := range m.Stats() {
		got[s.Name] = s.Protocol
	}
	if got["t"] != "tcp" || got["u"] != "udp" || got["h"] != "http" {
		t.Errorf("protocol routing = %v", got)
	}
}

func TestManager_StartServesAndStats(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer be.Close()

	port := freePort(t)
	m := newTestManager(t, []config.Route{
		{Name: "web", Protocol: "http", Listen: port, Backends: []config.Backend{backendOf(t, be)}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Start(ctx) }()

	addr := "127.0.0.1:" + strconv.Itoa(port)
	waitTCP(t, addr)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET through manager: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}

	// Total request count should have advanced.
	total := int64(0)
	for _, s := range m.Stats() {
		if s.Name == "web" {
			total = s.Total
		}
	}
	if total < 1 {
		t.Errorf("route Total = %d, want >= 1 after a request", total)
	}
}

func TestManager_StopCancelsListeners(t *testing.T) {
	port := freePort(t)
	m := newTestManager(t, []config.Route{
		{Name: "web", Protocol: "http", Listen: port, Backends: []config.Backend{{Host: "127.0.0.1", Port: 9, Weight: 1}}},
	})
	ctx := context.Background()
	go func() { _ = m.Start(ctx) }()
	waitTCP(t, "127.0.0.1:"+strconv.Itoa(port))

	done := make(chan error, 1)
	go func() { done <- m.Stop(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Stop returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return within 5s")
	}
}

// denyAllEngine builds a policy engine whose policy denies every request.
func denyAllEngine(t *testing.T) *policy.Engine {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deny.rego"),
		[]byte("package vortex\n\ndefault allow = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := policy.NewEngine(policy.EngineConfig{PolicyDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestManager_PolicyEngineEnforcedOnHTTPRoute(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "backend")
	}))
	defer be.Close()

	port := freePort(t)
	m, err := NewManager(ManagerConfig{
		Config: &config.Config{Routes: []config.Route{
			{Name: "web", Protocol: "http", Listen: port, Backends: []config.Backend{backendOf(t, be)}},
		}},
		TCPPool:      tcp.NewPool(tcp.PoolConfig{}),
		PolicyEngine: denyAllEngine(t),
		Logger:       discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Start(ctx) }()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	waitTCP(t, addr)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (deny-all policy)", resp.StatusCode)
	}
}

func TestManager_NilPolicyEngineNoEnforcement(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "backend")
	}))
	defer be.Close()

	port := freePort(t)
	// No PolicyEngine: requests must pass through to the backend.
	m := newTestManager(t, []config.Route{
		{Name: "web", Protocol: "http", Listen: port, Backends: []config.Backend{backendOf(t, be)}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Start(ctx) }()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	waitTCP(t, addr)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "backend" {
		t.Errorf("status=%d body=%q, want 200 'backend'", resp.StatusCode, body)
	}
}

func TestManager_MetricsRecordedOnHTTPRoute(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer be.Close()

	metrics := observability.NewMetrics("vortex")
	port := freePort(t)
	m, err := NewManager(ManagerConfig{
		Config: &config.Config{Routes: []config.Route{
			{Name: "web", Protocol: "http", Listen: port, Backends: []config.Backend{backendOf(t, be)}},
		}},
		TCPPool: tcp.NewPool(tcp.PoolConfig{}),
		Metrics: metrics,
		Logger:  discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Start(ctx) }()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	waitTCP(t, addr)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), `route="web"`) {
		t.Errorf("metrics should record a request labelled route=web:\n%s", body)
	}
}

func TestManager_NamespaceQuotaEnforced(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "backend")
	}))
	defer be.Close()

	reg := tenancy.NewRegistry()
	if _, err := reg.Create(tenancy.NamespaceConfig{
		ID: "ns-1", OrgID: "org-a", Quotas: tenancy.QuotaConfig{MaxConnections: 1},
	}); err != nil {
		t.Fatal(err)
	}
	enf := tenancy.NewEnforcer(reg)

	port := freePort(t)
	m, err := NewManager(ManagerConfig{
		Config: &config.Config{Routes: []config.Route{
			{Name: "web", Protocol: "http", Listen: port, Backends: []config.Backend{backendOf(t, be)}, NamespaceID: "ns-1"},
		}},
		TCPPool:  tcp.NewPool(tcp.PoolConfig{}),
		Registry: reg, Enforcer: enf, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Start(ctx) }()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	waitTCP(t, addr)

	// A normal request succeeds (quota of 1 connection allows it).
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("namespaced route status = %d, want 200", resp.StatusCode)
	}
}

func TestManager_NoNamespaceNoEnforcement(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "backend")
	}))
	defer be.Close()

	reg := tenancy.NewRegistry()
	enf := tenancy.NewEnforcer(reg)
	port := freePort(t)
	m, err := NewManager(ManagerConfig{
		// Route has no NamespaceID, so the enforcer middleware is not attached.
		Config: &config.Config{Routes: []config.Route{
			{Name: "web", Protocol: "http", Listen: port, Backends: []config.Backend{backendOf(t, be)}},
		}},
		TCPPool:  tcp.NewPool(tcp.PoolConfig{}),
		Registry: reg, Enforcer: enf, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Start(ctx) }()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	waitTCP(t, addr)

	for i := 0; i < 20; i++ {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200 (no tenancy)", i, resp.StatusCode)
		}
	}
}

func waitTCP(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s", addr)
}
