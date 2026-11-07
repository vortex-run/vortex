package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sandeepbollavaram/vortex/internal/auth"
	"github.com/sandeepbollavaram/vortex/internal/config"
)

func TestAgentCatalog_ReturnsManifests(t *testing.T) {
	holder := config.NewHolder(&config.Config{Cluster: config.Cluster{Name: "test"}})
	s := New("127.0.0.1:0", holder, "v-test", discardLogger())
	rec := serve(s, loopbackReq(http.MethodGet, "/api/agents/catalog"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) < 8 {
		t.Fatalf("want >=8 catalog agents, got %d", len(body.Agents))
	}
	id, _ := body.Agents[0]["id"].(string)
	if id == "" {
		t.Fatal("catalog entries need id")
	}
}

func TestAgentCatalog_RequiresAPIKey(t *testing.T) {
	holder := config.NewHolder(&config.Config{Cluster: config.Cluster{Name: "test"}})
	s := New("127.0.0.1:0", holder, "v-test", discardLogger())
	keys := auth.NewAPIKeyStore()
	rbac := auth.NewRBAC()
	if _, _, err := keys.Issue("u", "default", []auth.Role{auth.RoleOperator}, "tok", 0); err != nil {
		t.Fatal(err)
	}
	s.SetAuth(auth.NewAuthMiddleware(keys, nil, rbac), keys, rbac)

	req := httptest.NewRequest(http.MethodGet, "/api/agents/catalog", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := serve(s, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("catalog without key = %d, want 401", rec.Code)
	}
}
