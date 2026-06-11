package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/config"
)

func TestLoopbackTrustEnabled(t *testing.T) {
	cases := map[string]bool{
		"":      true,
		"true":  true,
		"1":     true,
		"yes":   true,
		"false": false,
		"0":     false,
		"no":    false,
		"off":   false,
		"FALSE": false,
	}
	for val, want := range cases {
		t.Setenv("VORTEX_TRUST_LOOPBACK", val)
		if got := loopbackTrustEnabled(); got != want {
			t.Errorf("VORTEX_TRUST_LOOPBACK=%q: got %v, want %v", val, got, want)
		}
	}
}

func TestInternalShutdown_RequiresKeyEvenOnLoopback(t *testing.T) {
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	keys := auth.NewAPIKeyStore()
	rbac := auth.NewRBAC()
	_, secret, err := keys.Issue("op", "default", []auth.Role{auth.RoleAdmin}, "tok", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(auth.NewAuthMiddleware(keys, nil, rbac), keys, rbac)
	shutdownCalled := false
	s.SetShutdownFunc(func() { shutdownCalled = true })

	// Loopback, no key: shutdown must be refused (never honours loopback trust).
	req := httptest.NewRequest(http.MethodPost, "/internal/shutdown", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := serve(s, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("loopback shutdown without key = %d, want 401", rec.Code)
	}
	if shutdownCalled {
		t.Error("shutdown handler ran without a credential")
	}

	// With a valid admin key, shutdown is permitted.
	req = httptest.NewRequest(http.MethodPost, "/internal/shutdown", nil)
	req.Header.Set("X-API-Key", secret)
	req.RemoteAddr = "127.0.0.1:5555"
	rec = serve(s, req)
	if rec.Code == http.StatusUnauthorized {
		t.Errorf("shutdown with valid admin key = %d, want success", rec.Code)
	}

	// Loopback reload (less destructive) still bypasses with default trust.
	s.SetReloadFunc(func() error { return nil })
	req = httptest.NewRequest(http.MethodPost, "/internal/reload", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec = serve(s, req)
	if rec.Code == http.StatusUnauthorized {
		t.Error("loopback reload should be allowed under default loopback trust")
	}
}
