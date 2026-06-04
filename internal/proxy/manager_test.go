package proxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/internal/proxy/tcp"
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
