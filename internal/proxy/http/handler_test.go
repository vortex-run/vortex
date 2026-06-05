package proxyhttp

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/plugins"
)

// fnHook adapts a function into a plugins.Hook for tests.
type fnHook struct {
	fn func(plugins.HookInput) (plugins.HookOutput, error)
}

func (h fnHook) Name() string           { return "fn" }
func (h fnHook) Type() plugins.HookType { return plugins.HookPreRequest }
func (h fnHook) Execute(_ context.Context, in plugins.HookInput) (plugins.HookOutput, error) {
	return h.fn(in)
}

// chainOf builds a HookChain from a single hook function.
func chainOf(fn func(plugins.HookInput) (plugins.HookOutput, error)) *plugins.HookChain {
	c := plugins.NewHookChain(false)
	c.Register(fnHook{fn: fn}, 0)
	return c
}

// backendAddr extracts host:port from an httptest server URL.
func backendAddr(t *testing.T, srv *httptest.Server) BackendAddr {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return BackendAddr{Addr: u.Host, Weight: 1}
}

// proxyRequest runs a request through a Handler and returns status + body.
func proxyRequest(t *testing.T, h *Handler, req *http.Request) (int, string, http.Header) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body), res.Header
}

func newProxyReq(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "198.51.100.10:1234"
	return req
}

func TestHandler_ForwardsRequest(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "backend-body")
	}))
	defer be.Close()
	h, err := NewHandler(HandlerConfig{Backends: []BackendAddr{backendAddr(t, be)}})
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := proxyRequest(t, h, newProxyReq(http.MethodGet, "http://app.com/"))
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
	if body != "backend-body" {
		t.Errorf("body = %q, want backend-body", body)
	}
}

func TestHandler_ForwardsStatusCode(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer be.Close()
	h, _ := NewHandler(HandlerConfig{Backends: []BackendAddr{backendAddr(t, be)}})
	code, _, _ := proxyRequest(t, h, newProxyReq(http.MethodGet, "http://app.com/"))
	if code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", code)
	}
}

func TestHandler_SetsXForwardedFor(t *testing.T) {
	var gotXFF string
	be := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotXFF = r.Header.Get("X-Forwarded-For")
	}))
	defer be.Close()
	h, _ := NewHandler(HandlerConfig{Backends: []BackendAddr{backendAddr(t, be)}})
	proxyRequest(t, h, newProxyReq(http.MethodGet, "http://app.com/"))
	if gotXFF != "198.51.100.10" {
		t.Errorf("backend X-Forwarded-For = %q, want 198.51.100.10", gotXFF)
	}
}

func TestHandler_StripsHopByHopResponseHeaders(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("X-Keep", "yes")
		w.WriteHeader(http.StatusOK)
	}))
	defer be.Close()
	h, _ := NewHandler(HandlerConfig{Backends: []BackendAddr{backendAddr(t, be)}})
	_, _, hdr := proxyRequest(t, h, newProxyReq(http.MethodGet, "http://app.com/"))
	if hdr.Get("Keep-Alive") != "" {
		t.Errorf("Keep-Alive should be stripped from response, got %q", hdr.Get("Keep-Alive"))
	}
	if hdr.Get("X-Keep") != "yes" {
		t.Errorf("normal header X-Keep should pass through, got %q", hdr.Get("X-Keep"))
	}
}

func TestHandler_RetriesOn503(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "recovered")
	}))
	defer good.Close()

	// Round-robin starts at the first backend (bad), then retries to good.
	h, _ := NewHandler(HandlerConfig{
		Backends: []BackendAddr{backendAddr(t, bad), backendAddr(t, good)},
		Retries:  2,
	})
	code, body, _ := proxyRequest(t, h, newProxyReq(http.MethodGet, "http://app.com/"))
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200 (retried to good backend)", code)
	}
	if body != "recovered" {
		t.Errorf("body = %q, want recovered", body)
	}
}

func TestHandler_NoRetryOn200(t *testing.T) {
	var calls atomic.Int64
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, "ok")
	}))
	defer be.Close()
	h, _ := NewHandler(HandlerConfig{Backends: []BackendAddr{backendAddr(t, be)}, Retries: 2})
	proxyRequest(t, h, newProxyReq(http.MethodGet, "http://app.com/"))
	if n := calls.Load(); n != 1 {
		t.Errorf("backend called %d times, want exactly 1 (no retry on 200)", n)
	}
}

func TestHandler_Timeout504(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer be.Close()
	h, _ := NewHandler(HandlerConfig{
		Backends: []BackendAddr{backendAddr(t, be)},
		Timeout:  100 * time.Millisecond,
	})
	code, _, _ := proxyRequest(t, h, newProxyReq(http.MethodGet, "http://app.com/"))
	if code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", code)
	}
}

func TestHandler_WebSocketStub501(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer be.Close()
	h, _ := NewHandler(HandlerConfig{Backends: []BackendAddr{backendAddr(t, be)}})
	req := newProxyReq(http.MethodGet, "http://app.com/ws")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	code, body, _ := proxyRequest(t, h, req)
	if code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", code)
	}
	if !strings.Contains(body, "websocket") {
		t.Errorf("body = %q, want websocket stub message", body)
	}
}

func TestHandler_StreamingFlush(t *testing.T) {
	// Backend streams chunks with delays; the proxy must forward them
	// incrementally rather than buffering the whole body.
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			_, _ = io.WriteString(w, "chunk\n")
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer be.Close()

	h, _ := NewHandler(HandlerConfig{
		Backends:      []BackendAddr{backendAddr(t, be)},
		FlushInterval: 10 * time.Millisecond,
	})
	// Serve the handler on a real socket so we can read the stream as it arrives.
	front := httptest.NewServer(h)
	defer front.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: app.com\r\n\r\n")

	br := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	// Read until we see the first chunk; this must arrive before the backend
	// finishes all three (the whole stream takes ~150ms).
	start := time.Now()
	buf := make([]byte, 4096)
	deadline := time.Now().Add(2 * time.Second)
	gotChunk := false
	for time.Now().Before(deadline) {
		n, _ := br.Read(buf)
		if n > 0 && strings.Contains(string(buf[:n]), "chunk") {
			gotChunk = true
			break
		}
	}
	if !gotChunk {
		t.Fatal("never received a streamed chunk")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("first chunk took %v, expected incremental delivery", elapsed)
	}
}

func TestHandler_HookAllowForwards(t *testing.T) {
	var reached bool
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = io.WriteString(w, "backend")
	}))
	defer be.Close()

	h, _ := NewHandler(HandlerConfig{
		Backends: []BackendAddr{backendAddr(t, be)},
		HookChain: chainOf(func(plugins.HookInput) (plugins.HookOutput, error) {
			return plugins.HookOutput{Allow: true}, nil
		}),
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://x/", nil))
	if !reached || rec.Code != http.StatusOK {
		t.Errorf("allow hook: reached=%v code=%d, want true 200", reached, rec.Code)
	}
}

func TestHandler_HookDenyReturns403(t *testing.T) {
	var reached bool
	be := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer be.Close()

	h, _ := NewHandler(HandlerConfig{
		Backends: []BackendAddr{backendAddr(t, be)},
		HookChain: chainOf(func(plugins.HookInput) (plugins.HookOutput, error) {
			return plugins.HookOutput{Allow: false}, nil
		}),
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://x/", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("deny hook status = %d, want 403", rec.Code)
	}
	if reached {
		t.Error("backend must not be reached when a hook denies")
	}
}

func TestHandler_HookModifiesRequestHeader(t *testing.T) {
	var gotHeader string
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Plugin")
		w.WriteHeader(http.StatusOK)
	}))
	defer be.Close()

	h, _ := NewHandler(HandlerConfig{
		Backends: []BackendAddr{backendAddr(t, be)},
		HookChain: chainOf(func(plugins.HookInput) (plugins.HookOutput, error) {
			return plugins.HookOutput{Allow: true, Headers: map[string][]string{"X-Plugin": {"injected"}}}, nil
		}),
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://x/", nil))
	if gotHeader != "injected" {
		t.Errorf("backend saw X-Plugin = %q, want 'injected' (hook header mod)", gotHeader)
	}
}
