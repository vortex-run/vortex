package studio

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingAudit captures audit appends for assertions.
type recordingAudit struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingAudit) Append(_ context.Context, _, action, resource string, _ map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, action+":"+resource)
	return nil
}

func (r *recordingAudit) has(actionPrefix string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if strings.HasPrefix(c, actionPrefix) {
			return true
		}
	}
	return false
}

func TestNewTerminalManager_Defaults(t *testing.T) {
	m := NewTerminalManager(TerminalConfig{})
	if m.cfg.MaxSessions != 5 || m.cfg.IdleTimeout != 30*time.Minute {
		t.Errorf("defaults: MaxSessions=%d IdleTimeout=%v", m.cfg.MaxSessions, m.cfg.IdleTimeout)
	}
	if m.cfg.Shell == "" {
		t.Error("default shell should be set")
	}
}

func TestTerminal_RejectsUnauthenticated(t *testing.T) {
	m := NewTerminalManager(TerminalConfig{
		Logger:    discardLogger(),
		Authorize: func(*http.Request) bool { return false },
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/studio/terminal", nil)
	wsUpgradeHeaders(req)
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated = %d, want 401", rec.Code)
	}
}

func TestTerminal_NonUpgradeRejected(t *testing.T) {
	m := NewTerminalManager(TerminalConfig{Logger: discardLogger()})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/studio/terminal", nil) // no upgrade headers
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-upgrade = %d, want 400", rec.Code)
	}
}

func TestTerminal_HandshakeAndAudit(t *testing.T) {
	rec := &recordingAudit{}
	shell, args := echoShell()
	m := NewTerminalManager(TerminalConfig{
		Logger:   discardLogger(),
		AuditLog: rec,
		Shell:    shell,
	})
	_ = args
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()

	conn := dialWS(t, srv.URL)
	defer func() { _ = conn.Close() }()

	// A session-start audit entry should appear shortly after connect.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !rec.has("studio.terminal.start") {
		time.Sleep(10 * time.Millisecond)
	}
	if !rec.has("studio.terminal.start") {
		t.Error("expected a studio.terminal.start audit entry")
	}
}

func TestTerminal_MaxSessionsEnforced(t *testing.T) {
	m := NewTerminalManager(TerminalConfig{
		Logger:      discardLogger(),
		MaxSessions: 1,
		Shell:       sleepShell(),
	})
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()

	c1 := dialWS(t, srv.URL)
	defer func() { _ = c1.Close() }()
	// Wait until the first session is registered.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && m.SessionCount() < 1 {
		time.Sleep(10 * time.Millisecond)
	}

	// The second upgrade must be rejected with 503 (cap = 1).
	resp := rawWSRequest(t, srv.URL)
	if resp != http.StatusServiceUnavailable {
		t.Errorf("second session status = %d, want 503", resp)
	}
}

func TestTerminal_CloseSessions(t *testing.T) {
	m := NewTerminalManager(TerminalConfig{Logger: discardLogger(), Shell: sleepShell()})
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()

	c := dialWS(t, srv.URL)
	defer func() { _ = c.Close() }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && m.SessionCount() < 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if m.SessionCount() != 1 {
		t.Fatalf("SessionCount = %d, want 1", m.SessionCount())
	}
	if err := m.CloseSessions(); err != nil {
		t.Fatalf("CloseSessions: %v", err)
	}
	if m.SessionCount() != 0 {
		t.Errorf("SessionCount after close = %d, want 0", m.SessionCount())
	}
}

// --- test helpers -----------------------------------------------------------

// echoShell returns a shell that stays alive reading stdin (any OS), so the
// session persists long enough for assertions.
func echoShell() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", nil
	}
	return "/bin/sh", nil
}

// sleepShell returns a long-running shell so the session stays open during the
// test. cmd.exe / sh both block reading stdin when given no input.
func sleepShell() string {
	if runtime.GOOS == "windows" {
		return "cmd.exe"
	}
	return "/bin/sh"
}

func wsUpgradeHeaders(req *http.Request) {
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", wsKey())
}

func wsKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

// dialWS performs a raw WebSocket handshake and returns a wsConn (client side).
func dialWS(t *testing.T, serverURL string) *wsConn {
	t.Helper()
	host := strings.TrimPrefix(serverURL, "http://")
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	req := "GET /studio/terminal HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: " + wsKey() + "\r\n\r\n"
	if _, err := rw.WriteString(req); err != nil {
		t.Fatal(err)
	}
	_ = rw.Flush()
	// Read the 101 status line + headers.
	line, err := rw.ReadString('\n')
	if err != nil || !strings.Contains(line, "101") {
		t.Fatalf("handshake response = %q err=%v, want 101", line, err)
	}
	for {
		h, herr := rw.ReadString('\n')
		if herr != nil {
			t.Fatal(herr)
		}
		if h == "\r\n" {
			break
		}
	}
	return &wsConn{conn: conn, rw: rw}
}

// rawWSRequest sends a WS upgrade and returns the HTTP status code (used to
// assert rejections like 503).
func rawWSRequest(t *testing.T, serverURL string) int {
	t.Helper()
	host := strings.TrimPrefix(serverURL, "http://")
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	req := "GET /studio/terminal HTTP/1.1\r\nHost: " + host +
		"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " +
		wsKey() + "\r\n\r\n"
	_, _ = conn.Write([]byte(req))
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	// Parse "HTTP/1.1 503 ..." → 503
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 {
		t.Fatalf("bad status line: %q", line)
	}
	switch parts[1] {
	case "101":
		return http.StatusSwitchingProtocols
	case "503":
		return http.StatusServiceUnavailable
	case "401":
		return http.StatusUnauthorized
	default:
		return http.StatusOK
	}
}
