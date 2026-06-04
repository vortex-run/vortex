package proxygateway

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func wsReq(t *testing.T) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://app.com/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.RemoteAddr = "203.0.113.9:5555"
	return req
}

func TestIsWebSocketUpgrade_Valid(t *testing.T) {
	if !IsWebSocketUpgrade(wsReq(t)) {
		t.Error("valid WS request should be detected")
	}
}

func TestIsWebSocketUpgrade_PlainHTTP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://app.com/", nil)
	if IsWebSocketUpgrade(req) {
		t.Error("plain HTTP request should not be a WS upgrade")
	}
}

func TestIsWebSocketUpgrade_MissingConnection(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://app.com/", nil)
	req.Header.Set("Upgrade", "websocket")
	// No Connection: Upgrade header.
	if IsWebSocketUpgrade(req) {
		t.Error("WS upgrade requires Connection: Upgrade")
	}
}

func TestIsWebSocketUpgrade_CaseInsensitive(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://app.com/", nil)
	req.Header.Set("Upgrade", "WebSocket")
	req.Header.Set("Connection", "keep-alive, Upgrade")
	if !IsWebSocketUpgrade(req) {
		t.Error("Upgrade/Connection matching should be case-insensitive")
	}
}

func TestStickySession_GetMiss(t *testing.T) {
	s := NewStickySession()
	if _, ok := s.Get("1.2.3.4"); ok {
		t.Error("Get on empty table should miss")
	}
}

func TestStickySession_SetGet(t *testing.T) {
	s := NewStickySession()
	s.Set("1.2.3.4", "backend-a:8080")
	if got, ok := s.Get("1.2.3.4"); !ok || got != "backend-a:8080" {
		t.Errorf("Get = %q, %v; want backend-a:8080, true", got, ok)
	}
}

func TestStickySession_Delete(t *testing.T) {
	s := NewStickySession()
	s.Set("1.2.3.4", "b:1")
	s.Delete("1.2.3.4")
	if _, ok := s.Get("1.2.3.4"); ok {
		t.Error("entry should be gone after Delete")
	}
}

func TestStickySession_Concurrent(t *testing.T) {
	s := NewStickySession()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ip := "10.0.0." + string(rune('0'+n%10))
			s.Set(ip, "b")
			_, _ = s.Get(ip)
			if n%3 == 0 {
				s.Delete(ip)
			}
		}(i)
	}
	wg.Wait() // must not panic or race
	// Table remains usable afterward.
	s.Set("final", "b")
	if _, ok := s.Get("final"); !ok {
		t.Error("sticky table should still work after concurrent access")
	}
}

// hijackableRecorder is an httptest.ResponseRecorder that also supports Hijack
// by returning a pre-wired net.Conn (one end of a pipe).
type hijackableRecorder struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	rw := bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn))
	return h.conn, rw, nil
}

// rawWSBackend starts a TCP server that completes a WebSocket-style upgrade
// (responds 101) then echoes raw bytes. It is not a real WS framing
// implementation — just enough to exercise the proxy handshake + byte tunnel.
func rawWSBackend(t *testing.T) string {
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
				// Read the upgrade request headers up to the blank line.
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if line == "\r\n" {
						break
					}
				}
				// Respond with 101 Switching Protocols.
				_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n" +
					"Upgrade: websocket\r\nConnection: Upgrade\r\n\r\n"))
				// Echo subsequent bytes.
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

func TestProxyWebSocket_EndToEnd(t *testing.T) {
	backend := rawWSBackend(t)

	// A socketpair: clientEnd is what the test drives; serverEnd is handed to
	// the proxy as the hijacked client connection.
	serverEnd, clientEnd := net.Pipe()

	rec := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder(), conn: serverEnd}
	req := wsReq(t)

	proxyDone := make(chan error, 1)
	go func() { proxyDone <- ProxyWebSocket(rec, req, backend, nil) }()

	// Read the 101 the proxy relays to the client.
	cr := bufio.NewReader(clientEnd)
	_ = clientEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
	statusLine, err := cr.ReadString('\n')
	if err != nil {
		t.Fatalf("reading 101 status line: %v", err)
	}
	if !strings.Contains(statusLine, "101") {
		t.Fatalf("expected 101 status, got %q", statusLine)
	}
	// Drain the rest of the response headers.
	for {
		line, err := cr.ReadString('\n')
		if err != nil {
			t.Fatalf("draining headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	// Send a frame and expect it echoed back through the tunnel.
	if _, err := clientEnd.Write([]byte("hello-ws")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 8)
	_ = clientEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := cr.Read(got); err != nil {
		t.Fatalf("reading echo: %v", err)
	}
	if string(got) != "hello-ws" {
		t.Errorf("echo = %q, want hello-ws", got)
	}

	_ = clientEnd.Close()
	select {
	case <-proxyDone:
	case <-time.After(2 * time.Second):
		t.Error("ProxyWebSocket did not return after client close")
	}
}
