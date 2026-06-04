//go:build integration

package integration

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	proxygateway "github.com/vortex-run/vortex/internal/proxy/gateway"
	proxyhttp "github.com/vortex-run/vortex/internal/proxy/http"
)

func gwBackendAddr(t *testing.T, srv *httptest.Server) proxyhttp.BackendAddr {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return proxyhttp.BackendAddr{Addr: u.Host, Weight: 1}
}

// rawWSEcho starts a TCP server that completes a WebSocket-style upgrade and
// echoes subsequent bytes. Returns its host:port.
func rawWSEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				br := bufio.NewReader(conn)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if line == "\r\n" {
						break
					}
				}
				_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"))
				buf := make([]byte, 1024)
				for {
					n, err := br.Read(buf)
					if n > 0 {
						_, _ = conn.Write(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

// startGatewayServer fronts a Gateway with a real HTTP server on :0.
func startGatewayServer(t *testing.T, g *proxygateway.Gateway) (string, context.CancelFunc) {
	t.Helper()
	router := proxyhttp.NewRouter()
	router.Handle("/*", g)
	srv := proxyhttp.NewServer(proxyhttp.ServerConfig{Addr: "127.0.0.1:0", Router: router})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a := srv.Addr()
		if _, port, err := net.SplitHostPort(a); err == nil && port != "0" {
			if c, derr := net.DialTimeout("tcp", a, 50*time.Millisecond); derr == nil {
				_ = c.Close()
				t.Cleanup(cancel)
				return a, cancel
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatal("gateway server did not start")
	return "", cancel
}

func TestGateway_WebSocketProxied(t *testing.T) {
	wsBackend := rawWSEcho(t)
	httpBE := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "http")
	}))
	defer httpBE.Close()
	httpHandler, _ := proxyhttp.NewHandler(proxyhttp.HandlerConfig{Backends: []proxyhttp.BackendAddr{gwBackendAddr(t, httpBE)}})

	g, err := proxygateway.NewGateway(proxygateway.GatewayConfig{
		HTTPHandler: httpHandler,
		WSBackends:  []proxyhttp.BackendAddr{{Addr: wsBackend, Weight: 1}},
		Sticky:      proxygateway.NewStickySession(),
	})
	if err != nil {
		t.Fatal(err)
	}
	addr, _ := startGatewayServer(t, g)

	// Raw WebSocket client: open TCP, send the upgrade, read 101, echo bytes.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: app.com\r\n"+
		"Upgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")

	br := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("expected 101, got %q", status)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("draining headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	if _, err := io.WriteString(conn, "hello ws"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 8)
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("reading echo: %v", err)
	}
	if string(got) != "hello ws" {
		t.Errorf("echo = %q, want 'hello ws'", got)
	}
}

func TestGateway_GRPCProxied(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Trailer", "Grpc-Status")
		w.Header().Set("Content-Type", "application/grpc")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "resp")
		w.Header().Set("Grpc-Status", "0")
	}))
	defer be.Close()

	grpc, _ := proxygateway.NewGRPCProxy(proxygateway.GRPCProxyConfig{Backends: []proxyhttp.BackendAddr{gwBackendAddr(t, be)}})
	httpHandler, _ := proxyhttp.NewHandler(proxyhttp.HandlerConfig{Backends: []proxyhttp.BackendAddr{gwBackendAddr(t, be)}})
	g, _ := proxygateway.NewGateway(proxygateway.GatewayConfig{HTTPHandler: httpHandler, GRPCProxy: grpc})
	addr, _ := startGatewayServer(t, g)

	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/pkg.Service/M", nil)
	req.Header.Set("Content-Type", "application/grpc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Trailer.Get("Grpc-Status"); got != "0" {
		t.Errorf("Grpc-Status trailer = %q, want 0", got)
	}
}

func TestGateway_HTTPFallthrough(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "http ok")
	}))
	defer be.Close()
	httpHandler, _ := proxyhttp.NewHandler(proxyhttp.HandlerConfig{Backends: []proxyhttp.BackendAddr{gwBackendAddr(t, be)}})
	g, _ := proxygateway.NewGateway(proxygateway.GatewayConfig{HTTPHandler: httpHandler})
	addr, _ := startGatewayServer(t, g)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "http ok" {
		t.Errorf("body = %q, want 'http ok'", body)
	}
}
