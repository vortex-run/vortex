package tui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeVortex builds an httptest server emulating the management API. It records
// the last X-API-Key seen.
type fakeVortex struct {
	srv     *httptest.Server
	lastKey string
}

func newFakeVortex(t *testing.T) *fakeVortex {
	t.Helper()
	f := &fakeVortex{}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		f.lastKey = r.Header.Get("X-API-Key")
		_, _ = io.WriteString(w, `{"status":"ok","version":"v9","cluster_name":"c1","uptime":"2h","config_hash":"abc","routes":[{"name":"api","protocol":"https","listen":":0","active":3}]}`)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"node_id":"n1","trust_domain":"c1.vortex","tls_provider":"internal","secret_backend":"local","policy_default":true,"plugin_count":2,"audit_entry_count":42,"cluster_name":"c1","version":"v9"}`)
	})
	mux.HandleFunc("/api/agents/submit", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"response":"hello back","session_id":"s1"}`)
	})
	mux.HandleFunc("/internal/reload", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "# HELP x\n# TYPE x gauge\nvortex_cluster_members 1\nvortex_requests_total{route=\"api\"} 100\nvortex_active_connections{route=\"api\"} 5\n")
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func testClient(t *testing.T, f *fakeVortex, key string) *Client {
	t.Helper()
	return NewClient(ClientConfig{BaseURL: f.srv.URL, APIKey: key})
}

func TestClient_Health(t *testing.T) {
	f := newFakeVortex(t)
	c := testClient(t, f, "")
	h, err := c.Health()
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.ClusterName != "c1" || h.Version != "v9" || h.Uptime != "2h" {
		t.Errorf("health = %+v", h)
	}
	if len(h.Routes) != 1 || h.Routes[0].Name != "api" || h.Routes[0].Active != 3 {
		t.Errorf("routes = %+v", h.Routes)
	}
}

func TestClient_Status(t *testing.T) {
	f := newFakeVortex(t)
	c := testClient(t, f, "")
	s, err := c.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s.NodeID != "n1" || s.TrustDomain != "c1.vortex" || s.PluginCount != 2 || s.AuditCount != 42 {
		t.Errorf("status = %+v", s)
	}
}

func TestClient_Metrics(t *testing.T) {
	f := newFakeVortex(t)
	c := testClient(t, f, "")
	m, err := c.Metrics()
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if m.ClusterMembers != 1 {
		t.Errorf("cluster_members = %v, want 1", m.ClusterMembers)
	}
	if m.RequestsTotal["api"] != 100 {
		t.Errorf("requests_total[api] = %v, want 100", m.RequestsTotal["api"])
	}
	if m.ActiveConns["api"] != 5 {
		t.Errorf("active_conns[api] = %v, want 5", m.ActiveConns["api"])
	}
}

func TestClient_IsConnected(t *testing.T) {
	f := newFakeVortex(t)
	c := testClient(t, f, "")
	if !c.IsConnected() {
		t.Error("IsConnected should be true against a live server")
	}
}

func TestClient_IsConnectedDown(t *testing.T) {
	c := NewClient(ClientConfig{BaseURL: "http://127.0.0.1:1"})
	if c.IsConnected() {
		t.Error("IsConnected should be false against a dead server")
	}
}

func TestClient_HealthErrorWhenDown(t *testing.T) {
	c := NewClient(ClientConfig{BaseURL: "http://127.0.0.1:1"})
	if _, err := c.Health(); err == nil {
		t.Error("Health against a dead server should error")
	}
}

func TestClient_Submit(t *testing.T) {
	f := newFakeVortex(t)
	c := testClient(t, f, "")
	resp, err := c.Submit("hi", "s1")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if resp != "hello back" {
		t.Errorf("submit response = %q, want 'hello back'", resp)
	}
}

func TestClient_SubmitDownReturnsFriendlyMessage(t *testing.T) {
	c := NewClient(ClientConfig{BaseURL: "http://127.0.0.1:1"})
	resp, err := c.Submit("hi", "s1")
	if err != nil {
		t.Fatalf("Submit against a dead server should not error, got: %v", err)
	}
	if !strings.HasPrefix(resp, ConnectionErrorPrefix) {
		t.Errorf("Submit response = %q, want the connection-error notice", resp)
	}
}

func TestClient_AgentChatDownReturnsFriendlyMessage(t *testing.T) {
	c := NewClient(ClientConfig{BaseURL: "http://127.0.0.1:1"})
	resp, err := c.AgentChat("code-agent", "s1", "hi")
	if err != nil {
		t.Fatalf("AgentChat against a dead server should not error, got: %v", err)
	}
	if !strings.HasPrefix(resp, ConnectionErrorPrefix) {
		t.Errorf("AgentChat response = %q, want the connection-error notice", resp)
	}
}

func TestClient_Reload(t *testing.T) {
	f := newFakeVortex(t)
	c := testClient(t, f, "")
	if err := c.Reload(); err != nil {
		t.Errorf("Reload: %v", err)
	}
}

func TestClient_SendsAPIKey(t *testing.T) {
	f := newFakeVortex(t)
	c := testClient(t, f, "secret-key-123")
	if _, err := c.Health(); err != nil {
		t.Fatal(err)
	}
	if f.lastKey != "secret-key-123" {
		t.Errorf("server saw key %q, want secret-key-123", f.lastKey)
	}
}

func TestClient_LoadAPIKeyFromEnv(t *testing.T) {
	t.Setenv("VORTEX_API_KEY", "env-key")
	c := NewClient(ClientConfig{})
	if c.LoadAPIKey() != "env-key" {
		t.Errorf("LoadAPIKey = %q, want env-key", c.LoadAPIKey())
	}
}

func TestParsePrometheus(t *testing.T) {
	text := strings.Join([]string{
		"# HELP vortex_requests_total reqs",
		"# TYPE vortex_requests_total counter",
		`vortex_requests_total{route="api",method="GET"} 50`,
		`vortex_requests_total{route="api",method="POST"} 25`,
		`vortex_active_connections{route="web"} 3`,
		"vortex_cluster_members 2",
		"garbage line that should be ignored",
	}, "\n")
	d := parsePrometheus(text)
	if d.RequestsTotal["api"] != 75 { // 50 + 25 summed across methods
		t.Errorf("requests_total[api] = %v, want 75", d.RequestsTotal["api"])
	}
	if d.ActiveConns["web"] != 3 {
		t.Errorf("active_conns[web] = %v, want 3", d.ActiveConns["web"])
	}
	if d.ClusterMembers != 2 {
		t.Errorf("cluster_members = %v, want 2", d.ClusterMembers)
	}
}

func TestClient_LoadAPIKeyFromFile(t *testing.T) {
	// No env var; key only in the persisted setup file.
	t.Setenv("VORTEX_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // redirect UserConfigDir (Linux)
	t.Setenv("AppData", t.TempDir())         // redirect on Windows

	path := APIKeyFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("file-key-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := NewClient(ClientConfig{})
	if got := c.LoadAPIKey(); got != "file-key-123" {
		t.Errorf("LoadAPIKey from file = %q, want file-key-123", got)
	}
}

func TestClient_LoadAPIKeyEnvBeatsFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	path := APIKeyFilePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte("file-key"), 0o600)

	t.Setenv("VORTEX_API_KEY", "env-key")
	c := NewClient(ClientConfig{})
	if got := c.LoadAPIKey(); got != "env-key" {
		t.Errorf("env var should win over file: got %q", got)
	}
}

func TestClient_SubmitTimeoutIsLong(t *testing.T) {
	// Submit must tolerate a slow (multi-second) handler that the 5s default
	// would have killed. Server sleeps 6s then replies.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agents/submit", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(6 * time.Second)
		_, _ = io.WriteString(w, `{"response":"slow ok","session_id":"s1"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(ClientConfig{BaseURL: srv.URL, Timeout: 5 * time.Second}) // 5s default
	resp, err := c.Submit("hi", "s1")
	if err != nil {
		t.Fatalf("Submit should not time out at 6s (uses 180s): %v", err)
	}
	if resp != "slow ok" {
		t.Errorf("resp = %q, want 'slow ok'", resp)
	}
}

func TestClient_TimeoutConstants(t *testing.T) {
	if submitTimeout < 180*time.Second {
		t.Errorf("submitTimeout = %v, want >= 180s", submitTimeout)
	}
	if forgeStatusTimeout < 300*time.Second {
		t.Errorf("forgeStatusTimeout = %v, want >= 300s", forgeStatusTimeout)
	}
}

func TestClient_HealthStillUsesShortTimeout(t *testing.T) {
	// A slow /health must still fail fast (the 5s default applies to reads).
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(ClientConfig{BaseURL: srv.URL, Timeout: 500 * time.Millisecond})
	if _, err := c.Health(); err == nil {
		t.Error("Health should honour the short client timeout and fail on a 2s handler")
	}
}
