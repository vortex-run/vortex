package studio

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewCodeServer_NotInstalled(t *testing.T) {
	// A binary path that does not exist must yield ErrCodeServerNotInstalled.
	_, err := NewCodeServer(CodeServerConfig{BinaryPath: "/nonexistent/code-server"})
	if !errors.Is(err, ErrCodeServerNotInstalled) {
		t.Errorf("err = %v, want ErrCodeServerNotInstalled", err)
	}
}

func TestErrCodeServerNotInstalled_IsNonFatalSentinel(t *testing.T) {
	// Callers distinguish "not installed" (degrade) from other errors via
	// errors.Is — verify the sentinel wraps correctly.
	err := errors.Join(errors.New("other"), ErrCodeServerNotInstalled)
	if !errors.Is(err, ErrCodeServerNotInstalled) {
		t.Error("ErrCodeServerNotInstalled should be detectable via errors.Is")
	}
}

func TestCodeServer_IsRunningFalseBeforeStart(t *testing.T) {
	cs := newProxyOnlyCodeServer(t, "127.0.0.1:1")
	if cs.IsRunning() {
		t.Error("IsRunning should be false before Start")
	}
}

func TestCodeServer_ProxyHandlerHTTP(t *testing.T) {
	// Stand up a fake backend and point a CodeServer's proxy at it. This
	// exercises ProxyHandler without a real code-server binary.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "from code-server:"+r.URL.Path)
	}))
	defer backend.Close()

	cs := newProxyOnlyCodeServer(t, backendHost(t, backend))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/studio/editor", nil)
	cs.ProxyHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "from code-server") {
		t.Errorf("proxied body = %q, want backend response", rec.Body.String())
	}
}

func TestCodeServer_ProxyHandlerWebSocketUpgrade(t *testing.T) {
	// A backend that confirms the Upgrade header reaches it (httputil's reverse
	// proxy preserves Connection/Upgrade for WebSockets).
	var gotUpgrade string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUpgrade = r.Header.Get("Upgrade")
		// We can't complete a real WS handshake here; just confirm headers
		// flowed through and respond 101-ish.
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	defer backend.Close()

	cs := newProxyOnlyCodeServer(t, backendHost(t, backend))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/studio/", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	cs.ProxyHandler().ServeHTTP(rec, req)

	if gotUpgrade != "websocket" {
		t.Errorf("backend Upgrade header = %q, want websocket (proxy must preserve it)", gotUpgrade)
	}
}

// TestCodeServer_ProxyOverUnixSocket: the proxy reaches code-server through
// its private socket (no TCP listener) and keeps the client's Host header.
func TestCodeServer_ProxyOverUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "cs.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	var gotHost string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		_, _ = io.WriteString(w, "from socket:"+r.URL.Path)
	}), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	defer func() { _ = srv.Close() }()

	cs := &CodeServer{log: discardLogger(), socket: sock}
	if err := cs.waitReady(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://vortex.example/studio/editor", nil)
	cs.ProxyHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "from socket:/studio/editor" {
		t.Fatalf("proxy = %d %q, want 200 from the socket backend", rec.Code, rec.Body.String())
	}
	if gotHost != "vortex.example" {
		t.Errorf("backend Host = %q, want the client's host (code-server checks Origin against it)", gotHost)
	}
}

// TestCodeServer_ProxyStripsVortexCredentials: the caller's API key is not
// forwarded to code-server.
func TestCodeServer_ProxyStripsVortexCredentials(t *testing.T) {
	var auth, key string
	backend := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		auth, key = r.Header.Get("Authorization"), r.Header.Get("X-API-Key")
	}))
	defer backend.Close()

	cs := newProxyOnlyCodeServer(t, backendHost(t, backend))
	req := httptest.NewRequest(http.MethodGet, "/studio/", nil)
	req.Header.Set("Authorization", "Bearer vx_secret")
	req.Header.Set("X-API-Key", "vx_secret")
	cs.ProxyHandler().ServeHTTP(httptest.NewRecorder(), req)
	if auth != "" || key != "" {
		t.Errorf("backend saw Authorization=%q X-API-Key=%q, want both stripped", auth, key)
	}
}

// TestCodeServer_ListensOnPrivateSocket: off Windows, code-server gets a
// socket in a 0700 directory and no TCP address.
func TestCodeServer_ListensOnPrivateSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses a loopback port")
	}
	cs := &CodeServer{cfg: CodeServerConfig{WorkspaceDir: "/ws", DataDir: "/data"}, log: discardLogger()}
	if err := cs.chooseListener(); err != nil {
		t.Fatal(err)
	}
	defer cs.removeSocketDir()
	dir := cs.socketDir
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket dir mode = %o, want 700", perm)
	}
	got := strings.Join(cs.args(), " ")
	want := "--socket " + filepath.Join(dir, "code-server.sock") + " --auth none --user-data-dir /data /ws"
	if got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
	cs.removeSocketDir()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("socket dir still exists after removeSocketDir: %v", err)
	}
}

// TestCodeServer_WindowsPicksFreePort: on Windows the listener is loopback
// TCP on a free port, not a fixed 8080 that a route backend might use.
func TestCodeServer_WindowsPicksFreePort(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Unix uses a socket")
	}
	cs := &CodeServer{log: discardLogger()}
	if err := cs.chooseListener(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cs.addr, "127.0.0.1:") || cs.addr == "127.0.0.1:8080" {
		t.Errorf("addr = %q, want a free 127.0.0.1 port", cs.addr)
	}
	// A loopback port is reachable by every local account: password auth,
	// never --auth none (audit NV-02).
	if args := strings.Join(cs.args(), " "); !strings.HasPrefix(args, "--bind-addr "+cs.addr+" --auth password") || len(cs.password) != 64 {
		t.Errorf("args = %q", args)
	}
}

// newProxyOnlyCodeServer builds a CodeServer with its addr pointed at host,
// bypassing binary detection — for testing the proxy path in isolation.
func newProxyOnlyCodeServer(t *testing.T, host string) *CodeServer {
	t.Helper()
	return &CodeServer{cfg: CodeServerConfig{}, log: discardLogger(), addr: host}
}

// backendHost extracts host:port from an httptest server URL.
func backendHost(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}
