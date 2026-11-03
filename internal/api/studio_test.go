package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/config"
)

// TestStudio_AdminOnly: Studio runs a shell, git push and DB queries without
// the approval gate, so only admin keys reach it, from loopback too.
func TestStudio_AdminOnly(t *testing.T) {
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	keys := auth.NewAPIKeyStore()
	rbac := auth.NewRBAC()
	s.SetAuth(auth.NewAuthMiddleware(keys, nil, rbac), keys, rbac)
	s.SetStudioHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	want := map[auth.Role]int{
		auth.RoleAdmin:    http.StatusOK,
		auth.RoleOperator: http.StatusForbidden,
		auth.RoleViewer:   http.StatusForbidden,
		auth.RoleReadonly: http.StatusForbidden,
	}
	for role, code := range want {
		_, secret, err := keys.Issue("u-"+string(role), "default", []auth.Role{role}, "tok", 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, remote := range []string{"203.0.113.9:4444", "127.0.0.1:4444"} {
			req := httptest.NewRequest(http.MethodGet, "/studio/git/status", nil)
			req.RemoteAddr = remote
			req.Header.Set("X-API-Key", secret)
			if rec := serve(s, req); rec.Code != code {
				t.Errorf("role %s from %s: GET /studio/git/status = %d, want %d", role, remote, rec.Code, code)
			}
		}
	}

	// No key: 401 even from loopback (no control-plane bypass).
	req := httptest.NewRequest(http.MethodGet, "/studio/terminal", nil)
	req.RemoteAddr = "127.0.0.1:4444"
	if rec := serve(s, req); rec.Code != http.StatusUnauthorized {
		t.Errorf("no key from loopback: GET /studio/terminal = %d, want 401", rec.Code)
	}
}
