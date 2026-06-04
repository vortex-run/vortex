package proxygateway

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	proxyhttp "github.com/vortex-run/vortex/internal/proxy/http"
)

// pipeConns returns the two ends of an in-memory connection: serverEnd is given
// to the gateway as the hijacked client conn; clientEnd is driven by the test.
func pipeConns() (serverEnd, clientEnd net.Conn) { return net.Pipe() }

// readUntil101 reads from conn until it sees the 101 status line and the end of
// the response headers, returning true on success.
func readUntil101(t *testing.T, conn net.Conn) bool {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		return false
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return false
		}
		if line == "\r\n" {
			return true
		}
	}
}

// recordingHandler records whether it was invoked and writes a marker body.
type recordingHandler struct {
	called bool
	marker string
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.called = true
	_, _ = io.WriteString(w, h.marker)
}

func TestGateway_GRPCRouted(t *testing.T) {
	httpH := &recordingHandler{marker: "http"}
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.WriteHeader(http.StatusOK)
	}))
	defer be.Close()
	grpc, _ := NewGRPCProxy(GRPCProxyConfig{Backends: []proxyhttp.BackendAddr{beAddr(t, be)}})

	g, err := NewGateway(GatewayConfig{HTTPHandler: httpH, GRPCProxy: grpc})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, grpcReq(t, "application/grpc"))

	if httpH.called {
		t.Error("gRPC request must not reach the HTTP handler")
	}
	if g.Stats().GRPCRequests != 1 {
		t.Errorf("GRPCRequests = %d, want 1", g.Stats().GRPCRequests)
	}
}

func TestGateway_HTTPFallthrough(t *testing.T) {
	httpH := &recordingHandler{marker: "http-ok"}
	g, _ := NewGateway(GatewayConfig{HTTPHandler: httpH})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://app/", nil)
	g.ServeHTTP(rec, req)

	if !httpH.called {
		t.Error("plain HTTP request should reach the HTTP handler")
	}
	if rec.Body.String() != "http-ok" {
		t.Errorf("body = %q, want http-ok", rec.Body.String())
	}
	if g.Stats().HTTPRequests != 1 {
		t.Errorf("HTTPRequests = %d, want 1", g.Stats().HTTPRequests)
	}
}

func TestGateway_NilGRPCFallsThrough(t *testing.T) {
	httpH := &recordingHandler{marker: "http"}
	g, _ := NewGateway(GatewayConfig{HTTPHandler: httpH}) // no GRPCProxy

	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, grpcReq(t, "application/grpc"))

	if !httpH.called {
		t.Error("with nil GRPCProxy, gRPC request should fall through to HTTP handler")
	}
	if g.Stats().HTTPRequests != 1 {
		t.Errorf("HTTPRequests = %d, want 1 (gRPC fell through)", g.Stats().HTTPRequests)
	}
}

func TestGateway_WebSocketRouted(t *testing.T) {
	httpH := &recordingHandler{marker: "http"}
	backend := rawWSBackend(t)
	g, err := NewGateway(GatewayConfig{
		HTTPHandler: httpH,
		WSBackends:  []proxyhttp.BackendAddr{{Addr: backend, Weight: 1}},
		Sticky:      NewStickySession(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Drive a real upgrade via a hijackable recorder + pipe.
	serverEnd, clientEnd := pipeConns()
	defer func() { _ = clientEnd.Close() }()
	rec := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder(), conn: serverEnd}

	done := make(chan struct{})
	go func() { g.ServeHTTP(rec, wsReq(t)); close(done) }()

	// Read the 101 the gateway relays.
	if !readUntil101(t, clientEnd) {
		t.Fatal("did not receive 101 from gateway")
	}
	if httpH.called {
		t.Error("WebSocket request must not reach the HTTP handler")
	}
	_ = clientEnd.Close()
	<-done
}

func TestGateway_WebSocketSticky(t *testing.T) {
	httpH := &recordingHandler{marker: "http"}
	sticky := NewStickySession()
	backend := rawWSBackend(t)
	g, _ := NewGateway(GatewayConfig{
		HTTPHandler: httpH,
		WSBackends:  []proxyhttp.BackendAddr{{Addr: backend, Weight: 1}},
		Sticky:      sticky,
	})

	// First WS connection from a client IP records a sticky binding.
	serverEnd, clientEnd := pipeConns()
	rec := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder(), conn: serverEnd}
	done := make(chan struct{})
	go func() { g.ServeHTTP(rec, wsReq(t)); close(done) }()
	readUntil101(t, clientEnd)
	_ = clientEnd.Close()
	<-done

	// The client IP from wsReq is 203.0.113.9 — it must now have a sticky entry.
	if b, ok := sticky.Get("203.0.113.9"); !ok || b != backend {
		t.Errorf("sticky session = %q, %v; want %s, true", b, ok, backend)
	}
}

func TestGateway_NilHTTPHandlerError(t *testing.T) {
	if _, err := NewGateway(GatewayConfig{}); err == nil {
		t.Error("expected error when HTTPHandler is nil")
	}
}

func TestGateway_StatsHTTP(t *testing.T) {
	g, _ := NewGateway(GatewayConfig{HTTPHandler: &recordingHandler{}})
	for i := 0; i < 3; i++ {
		g.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://a/", nil))
	}
	if g.Stats().HTTPRequests != 3 {
		t.Errorf("HTTPRequests = %d, want 3", g.Stats().HTTPRequests)
	}
}

func TestGateway_StatsGRPC(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer be.Close()
	grpc, _ := NewGRPCProxy(GRPCProxyConfig{Backends: []proxyhttp.BackendAddr{beAddr(t, be)}})
	g, _ := NewGateway(GatewayConfig{HTTPHandler: &recordingHandler{}, GRPCProxy: grpc})

	for i := 0; i < 2; i++ {
		g.ServeHTTP(httptest.NewRecorder(), grpcReq(t, "application/grpc"))
	}
	if g.Stats().GRPCRequests != 2 {
		t.Errorf("GRPCRequests = %d, want 2", g.Stats().GRPCRequests)
	}
}
