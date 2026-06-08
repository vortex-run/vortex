package tui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
