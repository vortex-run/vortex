package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/vortex-run/vortex/internal/config"
)

func testHolder(t *testing.T) *config.Holder {
	t.Helper()
	p := filepath.Join(t.TempDir(), "vortex.cue")
	body := `
cluster: {name: "t"}
tls: {acme_email: "a@b.com"}
routes: []
security: {}
secrets: {}
observability: {}
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return config.NewHolder(cfg)
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s := New(":0", testHolder(t), "test", discardLogger())
	s.SetReloadFunc(func() error { return nil })
	s.SetShutdownFunc(func() {})
	return s
}

func doInternal(t *testing.T, handler http.HandlerFunc, remoteAddr string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/internal/x", nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec.Code
}

func TestInternalReloadFromLocalhost(t *testing.T) {
	s := newTestServer(t)
	if code := doInternal(t, s.handleInternalReload, "127.0.0.1:54321"); code != http.StatusOK {
		t.Errorf("reload from localhost = %d, want 200", code)
	}
}

func TestInternalReloadFromExternalIP(t *testing.T) {
	s := newTestServer(t)
	if code := doInternal(t, s.handleInternalReload, "198.51.100.4:54321"); code != http.StatusForbidden {
		t.Errorf("reload from external IP = %d, want 403", code)
	}
}

func TestInternalShutdownFromLocalhost(t *testing.T) {
	s := newTestServer(t)
	if code := doInternal(t, s.handleInternalShutdown, "127.0.0.1:54321"); code != http.StatusOK {
		t.Errorf("shutdown from localhost = %d, want 200", code)
	}
}

func TestInternalShutdownFromExternalIP(t *testing.T) {
	s := newTestServer(t)
	if code := doInternal(t, s.handleInternalShutdown, "203.0.113.9:54321"); code != http.StatusForbidden {
		t.Errorf("shutdown from external IP = %d, want 403", code)
	}
}
