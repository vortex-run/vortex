package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// authedServer builds an in-memory authed server with an admin key.
func authedServer(t *testing.T) (*Server, string) {
	t.Helper()
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	keys := auth.NewAPIKeyStore()
	rbac := auth.NewRBAC()
	_, secret, err := keys.Issue("op", "default", []auth.Role{auth.RoleAdmin}, "tok", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(auth.NewAuthMiddleware(keys, nil, rbac), keys, rbac)
	return s, secret
}

func TestInternalShutdown_LoopbackTrustedByDefault(t *testing.T) {
	// Default loopback trust: `vortex stop` posts keyless from the same host
	// and must succeed (the SIGTERM equivalent).
	t.Setenv("VORTEX_TRUST_LOOPBACK", "")
	s, _ := authedServer(t)
	// The handler runs shutdownFunc in a goroutine after responding, so signal
	// via a channel rather than a bool to avoid a race.
	called := make(chan struct{}, 1)
	s.SetShutdownFunc(func() { called <- struct{}{} })

	req := httptest.NewRequest(http.MethodPost, "/internal/shutdown", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := serve(s, req)
	if rec.Code == http.StatusUnauthorized {
		t.Errorf("loopback shutdown = %d, want allowed under default trust", rec.Code)
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Error("loopback shutdown should invoke the shutdown handler by default")
	}
}

func TestInternalShutdown_RequiresKeyWhenLoopbackUntrusted(t *testing.T) {
	// Behind a same-host proxy operators disable loopback trust; then shutdown
	// requires a credential (production audit M1).
	t.Setenv("VORTEX_TRUST_LOOPBACK", "false")
	s, secret := authedServer(t)
	called := false
	s.SetShutdownFunc(func() { called = true })

	// Loopback, no key → refused.
	req := httptest.NewRequest(http.MethodPost, "/internal/shutdown", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := serve(s, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("untrusted-loopback shutdown without key = %d, want 401", rec.Code)
	}
	if called {
		t.Error("shutdown handler ran without a credential")
	}

	// With a valid admin key → permitted.
	req = httptest.NewRequest(http.MethodPost, "/internal/shutdown", nil)
	req.Header.Set("X-API-Key", secret)
	req.RemoteAddr = "127.0.0.1:5555"
	rec = serve(s, req)
	if rec.Code == http.StatusUnauthorized {
		t.Errorf("shutdown with valid admin key = %d, want success", rec.Code)
	}
}
