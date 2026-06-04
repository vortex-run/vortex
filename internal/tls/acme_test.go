package vtls

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newACME(t *testing.T, cfg ACMEConfig) *ACMEManager {
	t.Helper()
	if cfg.Store == nil {
		cfg.Store = newStore(t)
	}
	if cfg.Email == "" {
		cfg.Email = "ops@example.com"
	}
	m, err := NewACMEManager(cfg)
	if err != nil {
		t.Fatalf("NewACMEManager: %v", err)
	}
	return m
}

func TestACME_EmptyEmailError(t *testing.T) {
	if _, err := NewACMEManager(ACMEConfig{Store: newStore(t)}); err == nil {
		t.Error("expected error for empty Email")
	}
}

func TestACME_NilStoreError(t *testing.T) {
	if _, err := NewACMEManager(ACMEConfig{Email: "a@b.com"}); err == nil {
		t.Error("expected error for nil Store")
	}
}

func TestACME_StagingURL(t *testing.T) {
	m := newACME(t, ACMEConfig{Staging: true})
	if m.mgr.Client.DirectoryURL != LetsEncryptStagingURL {
		t.Errorf("DirectoryURL = %q, want staging URL", m.mgr.Client.DirectoryURL)
	}
}

func TestACME_ProductionURLByDefault(t *testing.T) {
	m := newACME(t, ACMEConfig{})
	if m.mgr.Client.DirectoryURL != LetsEncryptProductionURL {
		t.Errorf("DirectoryURL = %q, want production URL", m.mgr.Client.DirectoryURL)
	}
}

func TestACME_GetCertificateServesCachedCert(t *testing.T) {
	store := newStore(t)
	// Pre-populate the store with a valid (non-expiring) cert for the domain.
	cached := makeCert(t, "cached.example.com", 60*24*time.Hour)
	if err := store.Save("cached.example.com", cached); err != nil {
		t.Fatal(err)
	}
	m := newACME(t, ACMEConfig{Store: store})

	cert, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "cached.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate should serve cached cert offline: %v", err)
	}
	if cert.Leaf.SerialNumber.Cmp(cached.Leaf.SerialNumber) != 0 {
		t.Error("GetCertificate did not return the cached cert")
	}
}

func TestACME_HTTPHandlerNonNil(t *testing.T) {
	m := newACME(t, ACMEConfig{})
	if m.HTTPHandler(nil) == nil {
		t.Error("HTTPHandler returned nil")
	}
}

func TestACME_HTTPHandlerFallthrough(t *testing.T) {
	m := newACME(t, ACMEConfig{})
	// A non-challenge request should hit our fallback handler.
	fallback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("fallback"))
	})
	h := m.HTTPHandler(fallback)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com/not-a-challenge", nil)
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "fallback" {
		t.Errorf("non-challenge request body = %q, want fallback", rec.Body.String())
	}
}

func TestACME_RenewalLoopNoPanicOnEmptyStore(t *testing.T) {
	m := newACME(t, ACMEConfig{})
	// renewAll must not panic when the store has no certs.
	m.renewAll()
}

func TestACME_RenewalAttemptsExpiringCert(t *testing.T) {
	store := newStore(t)
	// A cert expiring in 1 day, with a 30-day renew window → needs renewal.
	expiring := makeCert(t, "expiring.example.com", 24*time.Hour)
	if err := store.Save("expiring.example.com", expiring); err != nil {
		t.Fatal(err)
	}
	// Point the ACME directory at an unreachable URL so the renewal attempt
	// fails fast without real network access.
	m := newACME(t, ACMEConfig{Store: store, DirectoryURL: "http://127.0.0.1:1/directory"})

	// renewAll will try to obtain a fresh cert via ACME, which fails (no
	// reachable directory) — but it must not panic and must leave the previous
	// cert in place.
	m.renewAll()

	if _, err := store.Load("expiring.example.com"); err != nil {
		t.Errorf("expiring cert should remain after a failed renewal: %v", err)
	}
}
