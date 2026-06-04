package vtls

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func managerCfg(t *testing.T, provider string) ManagerConfig {
	t.Helper()
	return ManagerConfig{
		Provider:  provider,
		StorePath: filepath.Join(t.TempDir(), "certs"),
		StoreKey:  []byte("manager-test-key"),
		ACME:      ACMEConfig{Email: "ops@example.com"},
	}
}

func TestManager_UnknownProvider(t *testing.T) {
	if _, err := NewManager(managerCfg(t, "magic")); err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestManager_InternalCreatesLocalCA(t *testing.T) {
	m, err := NewManager(managerCfg(t, "internal"))
	if err != nil {
		t.Fatal(err)
	}
	if m.LocalCA() == nil {
		t.Error("internal provider should create a LocalCA")
	}
}

func TestManager_LetsEncryptCreatesACME(t *testing.T) {
	m, err := NewManager(managerCfg(t, "letsencrypt"))
	if err != nil {
		t.Fatal(err)
	}
	if m.acme == nil {
		t.Error("letsencrypt provider should create an ACME manager")
	}
	if m.LocalCA() != nil {
		t.Error("letsencrypt provider should not create a LocalCA")
	}
}

func TestManager_MinVersionTLS12(t *testing.T) {
	cfg := managerCfg(t, "internal")
	cfg.MinVersion = "TLS1.2"
	m, _ := NewManager(cfg)
	if v := m.TLSConfig().MinVersion; v != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS1.2", v)
	}
}

func TestManager_MinVersionTLS13(t *testing.T) {
	cfg := managerCfg(t, "internal")
	cfg.MinVersion = "TLS1.3"
	m, _ := NewManager(cfg)
	if v := m.TLSConfig().MinVersion; v != tls.VersionTLS13 {
		t.Errorf("MinVersion = %x, want TLS1.3", v)
	}
}

func TestManager_CipherSuitesExcludeRC4(t *testing.T) {
	m, _ := NewManager(managerCfg(t, "internal"))
	for _, cs := range m.TLSConfig().CipherSuites {
		if cs == tls.TLS_RSA_WITH_RC4_128_SHA || cs == tls.TLS_ECDHE_RSA_WITH_RC4_128_SHA {
			t.Errorf("cipher suites must not include RC4 (0x%04x)", cs)
		}
	}
}

func TestManager_CipherSuitesExclude3DES(t *testing.T) {
	m, _ := NewManager(managerCfg(t, "internal"))
	for _, cs := range m.TLSConfig().CipherSuites {
		if cs == tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA || cs == tls.TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA {
			t.Errorf("cipher suites must not include 3DES (0x%04x)", cs)
		}
	}
}

func TestManager_ChallengeHandlerNilForInternal(t *testing.T) {
	m, _ := NewManager(managerCfg(t, "internal"))
	if m.ChallengeHandler() != nil {
		t.Error("ChallengeHandler should be nil for internal provider")
	}
}

func TestManager_ChallengeHandlerNonNilForLetsEncrypt(t *testing.T) {
	m, _ := NewManager(managerCfg(t, "letsencrypt"))
	if m.ChallengeHandler() == nil {
		t.Error("ChallengeHandler should be non-nil for letsencrypt provider")
	}
}

func TestManager_FullHandshakeInternal(t *testing.T) {
	m, err := NewManager(managerCfg(t, "internal"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "manager ok")
	}))
	srv.TLS = m.TLSConfig()
	srv.StartTLS()
	defer srv.Close()

	pool := x509.NewCertPool()
	pool.AddCert(m.LocalCA().CACert())
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12},
	}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("HTTPS GET via manager: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "manager ok" {
		t.Errorf("body = %q, want 'manager ok'", body)
	}
}
