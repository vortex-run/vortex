package proxygateway_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sandeepbollavaram/vortex/internal/plugins"
	proxygateway "github.com/sandeepbollavaram/vortex/internal/proxy/gateway"
	proxyhttp "github.com/sandeepbollavaram/vortex/internal/proxy/http"
)

type denyHook struct{ calls *atomic.Int64 }

func (d denyHook) Name() string           { return "deny-all" }
func (d denyHook) Type() plugins.HookType { return plugins.HookPreRequest }
func (d denyHook) Execute(_ context.Context, _ plugins.HookInput) (plugins.HookOutput, error) {
	d.calls.Add(1)
	return plugins.HookOutput{Allow: false}, nil
}

// Audit F-09: a route's pre-request plugin deny must also refuse a WebSocket
// upgrade, before the backend is dialed. The gateway is composed exactly as
// Manager.buildHTTP composes it (PreUpgrade: handler.AllowUpgrade).
func TestGateway_WebSocketHonoursPluginDeny(t *testing.T) {
	var backendConns atomic.Int64
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			backendConns.Add(1)
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				if strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
					fmt.Fprintf(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\nWS-BACKEND-REACHED path=%s", req.URL.Path)
					time.Sleep(300 * time.Millisecond)
					return
				}
				fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: 14\r\nConnection: close\r\n\r\nPLAIN-BACKEND\n")
			}(c)
		}
	}()

	var calls atomic.Int64
	chain := plugins.NewHookChain(true)
	chain.Register(denyHook{calls: &calls}, 0)
	backends := []proxyhttp.BackendAddr{{Addr: ln.Addr().String(), Weight: 1}}
	h, err := proxyhttp.NewHandler(proxyhttp.HandlerConfig{
		Backends: backends, Balancer: "round-robin",
		RoundTripper: proxyhttp.NewRoundTripper(proxyhttp.RoundTripperConfig{}),
		HookChain:    chain, RouteName: "r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	gw, err := proxygateway.NewGateway(proxygateway.GatewayConfig{
		HTTPHandler: h, Sticky: proxygateway.NewStickySession(), WSBackends: backends,
		PreUpgrade: h.AllowUpgrade,
	})
	if err != nil {
		t.Fatal(err)
	}
	router := proxyhttp.NewRouter()
	router.Handle("/*", gw) // routePattern for a host-less route
	front := httptest.NewServer(router)
	defer front.Close()

	resp, err := http.Get(front.URL + "/ws")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plain GET: status %d, want 403", resp.StatusCode)
	}

	c, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprint(c, "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	wsResp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatalf("reading upgrade response: %v", err)
	}
	_ = wsResp.Body.Close()
	if wsResp.StatusCode != http.StatusForbidden {
		t.Errorf("websocket upgrade: status %d, want 403 (plugin deny bypassed)", wsResp.StatusCode)
	}
	if n := backendConns.Load(); n != 0 {
		t.Errorf("backend was dialed %d time(s) for denied requests", n)
	}
	if calls.Load() != 2 {
		t.Errorf("hook calls = %d, want 2 (plain + upgrade)", calls.Load())
	}
}
