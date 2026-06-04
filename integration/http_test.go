//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	proxyhttp "github.com/vortex-run/vortex/internal/proxy/http"
)

// beAddr returns the host:port of an httptest server.
func beAddr(t *testing.T, srv *httptest.Server) proxyhttp.BackendAddr {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return proxyhttp.BackendAddr{Addr: u.Host, Weight: 1}
}

// startProxy builds a Router→Handler→Server stack in front of the given
// backends and returns the bound front-end address plus a cancel func.
func startProxy(t *testing.T, hcfg proxyhttp.HandlerConfig, pattern string) (string, context.CancelFunc) {
	t.Helper()
	h, err := proxyhttp.NewHandler(hcfg)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	router := proxyhttp.NewRouter()
	router.Handle(pattern, h)
	srv := proxyhttp.NewServer(proxyhttp.ServerConfig{Addr: "127.0.0.1:0", Router: router})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		addr := srv.Addr()
		if _, port, err := net.SplitHostPort(addr); err == nil && port != "0" {
			if c, derr := net.DialTimeout("tcp", addr, 50*time.Millisecond); derr == nil {
				_ = c.Close()
				t.Cleanup(cancel)
				return addr, cancel
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatal("proxy never started listening")
	return "", cancel
}

func TestHTTP_ProxiesRequest(t *testing.T) {
	var gotXFF string
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXFF = r.Header.Get("X-Forwarded-For")
		_, _ = io.WriteString(w, "hello from backend")
	}))
	defer be.Close()

	addr, _ := startProxy(t, proxyhttp.HandlerConfig{Backends: []proxyhttp.BackendAddr{beAddr(t, be)}}, "/")

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from backend" {
		t.Errorf("body = %q, want 'hello from backend'", body)
	}
	if gotXFF == "" {
		t.Error("backend did not receive X-Forwarded-For")
	}
}

func TestHTTP_LoadBalancesTwoBackends(t *testing.T) {
	var a, b atomic.Int64
	be1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		a.Add(1)
		_, _ = io.WriteString(w, "1")
	}))
	defer be1.Close()
	be2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		b.Add(1)
		_, _ = io.WriteString(w, "2")
	}))
	defer be2.Close()

	addr, _ := startProxy(t, proxyhttp.HandlerConfig{
		Backends: []proxyhttp.BackendAddr{beAddr(t, be1), beAddr(t, be2)},
	}, "/")

	for i := 0; i < 200; i++ {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if a.Load() < 90 || a.Load() > 110 {
		t.Errorf("backend1 = %d, want ~100", a.Load())
	}
	if b.Load() < 90 || b.Load() > 110 {
		t.Errorf("backend2 = %d, want ~100", b.Load())
	}
}

func TestHTTP_RetriesOn503(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer good.Close()

	addr, _ := startProxy(t, proxyhttp.HandlerConfig{
		Backends: []proxyhttp.BackendAddr{beAddr(t, bad), beAddr(t, good)},
		Retries:  1,
	}, "/")

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (retried)", resp.StatusCode)
	}
}

func TestHTTP_RouteNotFound(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer be.Close()
	// Register a route for a specific host so an unknown host 404s.
	addr, _ := startProxy(t, proxyhttp.HandlerConfig{Backends: []proxyhttp.BackendAddr{beAddr(t, be)}}, "known.com")

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/x", nil)
	req.Host = "unknown.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("404 body not JSON: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Errorf("404 JSON missing error field: %v", body)
	}
}

func TestHTTP_GracefulShutdown(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = io.WriteString(w, "slow-done")
	}))
	defer be.Close()

	h, _ := proxyhttp.NewHandler(proxyhttp.HandlerConfig{
		Backends: []proxyhttp.BackendAddr{beAddr(t, be)},
		Timeout:  5 * time.Second,
	})
	router := proxyhttp.NewRouter()
	router.Handle("/", h)
	srv := proxyhttp.NewServer(proxyhttp.ServerConfig{Addr: "127.0.0.1:0", Router: router})
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() { returned <- srv.ListenAndServe(ctx) }()

	var addr string
	for i := 0; i < 150; i++ {
		a := srv.Addr()
		if _, port, err := net.SplitHostPort(a); err == nil && port != "0" {
			addr = a
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == "" {
		t.Fatal("server not listening")
	}

	resCh := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			resCh <- "ERR:" + err.Error()
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		resCh <- string(b)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel() // shut down while the slow request is in flight

	select {
	case got := <-resCh:
		if got != "slow-done" {
			t.Errorf("in-flight request result = %q, want slow-done (not dropped)", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request never completed")
	}
	select {
	case err := <-returned:
		if err != nil {
			t.Errorf("server exit error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not exit after cancel")
	}
}

func TestHTTP_WebSocketStub(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer be.Close()
	// Path-prefix route so /ws is matched and reaches the WebSocket stub.
	addr, _ := startProxy(t, proxyhttp.HandlerConfig{Backends: []proxyhttp.BackendAddr{beAddr(t, be)}}, "/*")

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "websocket") {
		t.Errorf("body = %q, want websocket stub", body)
	}
}
