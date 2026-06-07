package studio

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
