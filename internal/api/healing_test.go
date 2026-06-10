package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/config"
)

func healingServer(t *testing.T, provider func() HealingStatus) (*Server, string) {
	t.Helper()
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	keys := auth.NewAPIKeyStore()
	rbac := auth.NewRBAC()
	_, secret, err := keys.Issue("u", "default", []auth.Role{auth.RoleOperator}, "tok", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(auth.NewAuthMiddleware(keys, nil, rbac), keys, rbac)
	if provider != nil {
		s.SetHealingProvider(provider)
	}
	return s, secret
}

func TestAPI_HealingStatusReturnsProvider(t *testing.T) {
	s, secret := healingServer(t, func() HealingStatus {
		return HealingStatus{
			Healthy: true,
			Checks: []HealingCheck{
				{Name: "api", Healthy: true, LatencyMs: 12, LastCheck: time.Now()},
			},
			RecoveryStats: HealingRecoveryStats{TotalEvents: 2, ActionsExecuted: 1},
		}
	})
	req := httptest.NewRequest(http.MethodGet, "/api/healing/status", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body HealingStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Healthy || len(body.Checks) != 1 || body.Checks[0].Name != "api" {
		t.Errorf("healing status = %+v", body)
	}
	if body.RecoveryStats.TotalEvents != 2 {
		t.Errorf("recovery stats = %+v", body.RecoveryStats)
	}
}

func TestAPI_HealingStatusDefaultWhenNoProvider(t *testing.T) {
	s, secret := healingServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/healing/status", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body HealingStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !body.Healthy {
		t.Error("no provider should default to healthy:true")
	}
}

func TestAPI_HealingStatusRequiresAuth(t *testing.T) {
	s, _ := healingServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/healing/status", nil)
	req.RemoteAddr = "127.0.0.1:5555" // loopback must NOT bypass
	rec := serve(s, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("healing status without key = %d, want 401", rec.Code)
	}
}

func TestAPI_HealingEventsEmptyWithoutAudit(t *testing.T) {
	s, secret := healingServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/healing/events", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("events status = %d, want 200", rec.Code)
	}
	var body struct {
		Events []any `json:"events"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Events) != 0 {
		t.Errorf("no audit log → empty events, got %d", len(body.Events))
	}
}
