package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/vortex-run/vortex/internal/audit"
	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/internal/observability"
)

// dashServer builds a server with dashboard providers and an audit log wired,
// reachable from loopback without a key.
func dashServer(t *testing.T) *Server {
	t.Helper()
	holder := config.NewHolder(&config.Config{Cluster: config.Cluster{Name: "test-cluster"}})
	s := New("127.0.0.1:0", holder, "v-test", discardLogger())

	logPath := filepath.Join(t.TempDir(), "audit.log")
	al, err := audit.NewLog(logPath, []byte("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuditLog(al)

	s.SetStatusProvider(func() StatusInfo {
		return StatusInfo{NodeID: "abc123", TrustDomain: "test-cluster.vortex"}
	})
	s.SetSecretsProvider(func() []SecretStatus {
		return []SecretStatus{{Name: "DB_PASSWORD", Set: false}}
	})
	s.SetPluginsProvider(func() []PluginInfo { return nil })
	return s
}

// loopbackGet builds a loopback GET request (passes the protected gate).
func loopbackReq(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "127.0.0.1:5555"
	return req
}

func TestAPI_StatusReturnsNodeID(t *testing.T) {
	s := dashServer(t)
	rec := serve(s, loopbackReq(http.MethodGet, "/api/status"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body StatusInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.NodeID != "abc123" {
		t.Errorf("node_id = %q, want abc123", body.NodeID)
	}
}

func TestAPI_SecretsStatusNoValues(t *testing.T) {
	s := dashServer(t)
	rec := serve(s, loopbackReq(http.MethodGet, "/api/secrets/status"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// The response must contain set/unset state but never a "value" field.
	raw := rec.Body.String()
	if !contains(raw, `"name":"DB_PASSWORD"`) || !contains(raw, `"set":false`) {
		t.Errorf("secrets status missing expected fields: %s", raw)
	}
	if contains(raw, "value") {
		t.Errorf("secrets status must never include values: %s", raw)
	}
}

func TestAPI_PluginsEmptyList(t *testing.T) {
	s := dashServer(t)
	rec := serve(s, loopbackReq(http.MethodGet, "/api/plugins"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Plugins []PluginInfo `json:"plugins"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Plugins) != 0 {
		t.Errorf("plugins = %v, want empty", body.Plugins)
	}
}

func TestAPI_AuditEmptyInitially(t *testing.T) {
	s := dashServer(t)
	rec := serve(s, loopbackReq(http.MethodGet, "/api/audit"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Entries []audit.Entry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 0 {
		t.Errorf("entries = %v, want empty for a fresh log", body.Entries)
	}
}

func TestAPI_AuditVerifyValid(t *testing.T) {
	s := dashServer(t)
	rec := serve(s, loopbackReq(http.MethodPost, "/api/audit/verify"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Valid bool `json:"valid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Valid {
		t.Error("verify on a clean log should be valid")
	}
}

func TestAPI_StatusReturnsAllFields(t *testing.T) {
	holder := config.NewHolder(&config.Config{Cluster: config.Cluster{Name: "c1"}})
	s := New("127.0.0.1:0", holder, "v9", discardLogger())
	s.SetStatusProvider(func() StatusInfo {
		return StatusInfo{
			NodeID: "n1", TrustDomain: "c1.vortex", TLSProvider: "internal",
			SecretBackend: "local", PolicyDefault: true, PluginCount: 2,
			AuditEntryCount: 5, ClusterName: "c1", Version: "v9",
		}
	})
	rec := serve(s, loopbackReq(http.MethodGet, "/api/status"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var b StatusInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.NodeID != "n1" || b.TrustDomain != "c1.vortex" || b.TLSProvider != "internal" ||
		b.SecretBackend != "local" || !b.PolicyDefault || b.PluginCount != 2 ||
		b.AuditEntryCount != 5 || b.ClusterName != "c1" || b.Version != "v9" {
		t.Errorf("/api/status missing required fields: %+v", b)
	}
}

func TestAPI_AuditReturnsEntries(t *testing.T) {
	s := dashServer(t)
	// Append two entries, then confirm the API returns them.
	for i := 0; i < 2; i++ {
		if err := s.auditLog.Append(t.Context(), "tester", "test.event", "res", nil); err != nil {
			t.Fatal(err)
		}
	}
	rec := serve(s, loopbackReq(http.MethodGet, "/api/audit?limit=10"))
	var body struct {
		Entries []audit.Entry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(body.Entries))
	}
	if body.Entries[0].Actor != "tester" || body.Entries[0].Action != "test.event" {
		t.Errorf("entry shape wrong: %+v", body.Entries[0])
	}
}

func TestAPI_AuditLimitRespected(t *testing.T) {
	s := dashServer(t)
	for i := 0; i < 5; i++ {
		_ = s.auditLog.Append(t.Context(), "a", "e", "r", nil)
	}
	rec := serve(s, loopbackReq(http.MethodGet, "/api/audit?limit=3"))
	var body struct {
		Entries []audit.Entry `json:"entries"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Entries) != 3 {
		t.Errorf("limit=3 returned %d entries, want 3", len(body.Entries))
	}
}

func TestAPI_MetricsPrometheusFormat(t *testing.T) {
	holder := config.NewHolder(&config.Config{Cluster: config.Cluster{Name: "c"}})
	s := New("127.0.0.1:0", holder, "v", discardLogger())
	s.SetMetricsHandler(observability.NewMetrics("vortex").Handler())

	rec := serve(s, loopbackReq(http.MethodGet, "/metrics"))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// Prometheus exposition: HELP/TYPE comment lines and a vortex_ metric.
	if !contains(body, "# HELP") || !contains(body, "# TYPE") || !contains(body, "vortex_") {
		t.Errorf("/metrics not Prometheus format:\n%s", body[:min(len(body), 300)])
	}
}

// contains is a tiny substring helper.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
