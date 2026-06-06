package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/vortex-run/vortex/internal/audit"
	"github.com/vortex-run/vortex/internal/config"
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
