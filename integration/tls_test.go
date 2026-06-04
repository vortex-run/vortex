//go:build integration

package integration

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	vtls "github.com/vortex-run/vortex/internal/tls"
)

func internalManager(t *testing.T, minVersion string) *vtls.Manager {
	t.Helper()
	m, err := vtls.NewManager(vtls.ManagerConfig{
		Provider:   "internal",
		StorePath:  filepath.Join(t.TempDir(), "certs"),
		StoreKey:   []byte("integration-key"),
		MinVersion: minVersion,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// startTLSServer starts an httptest TLS server using the manager's TLS config.
func startTLSServer(t *testing.T, m *vtls.Manager, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	srv.TLS = m.TLSConfig()
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// caTrustClient builds a client that trusts the manager's local CA.
func caTrustClient(m *vtls.Manager, minVer, maxVer uint16) *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(m.LocalCA().CACert())
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			ServerName: "localhost",
			MinVersion: minVer,
			MaxVersion: maxVer,
		},
	}}
}

func TestTLS_LocalCA_HTTPSHandshake(t *testing.T) {
	m := internalManager(t, "TLS1.2")
	srv := startTLSServer(t, m, "tls ok")

	resp, err := caTrustClient(m, tls.VersionTLS12, tls.VersionTLS13).Get(srv.URL)
	if err != nil {
		t.Fatalf("HTTPS GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "tls ok" {
		t.Errorf("body = %q, want 'tls ok'", body)
	}
	// The LocalCA issues ECDSA certificates.
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		t.Fatal("no peer certificate in TLS state")
	}
	if pk := resp.TLS.PeerCertificates[0].PublicKeyAlgorithm; pk != x509.ECDSA {
		t.Errorf("peer cert public key alg = %v, want ECDSA", pk)
	}
}

func TestTLS_CertCached(t *testing.T) {
	m := internalManager(t, "TLS1.2")
	srv := startTLSServer(t, m, "ok")
	client := caTrustClient(m, tls.VersionTLS12, tls.VersionTLS13)

	resp1, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp1.Body.Close()
	serial1 := resp1.TLS.PeerCertificates[0].SerialNumber

	resp2, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	serial2 := resp2.TLS.PeerCertificates[0].SerialNumber

	if serial1.Cmp(serial2) != 0 {
		t.Errorf("cert serial differs between requests (%s vs %s); cert should be cached", serial1, serial2)
	}
}

func TestTLS_MinVersionTLS12_RejectsTLS11(t *testing.T) {
	m := internalManager(t, "TLS1.2")
	srv := startTLSServer(t, m, "ok")

	// Client capped at TLS 1.1 must fail against a TLS-1.2-minimum server.
	client := caTrustClient(m, tls.VersionTLS10, tls.VersionTLS11)
	resp, err := client.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Error("TLS 1.1 client should be rejected by a TLS-1.2-minimum server")
	}
}

func TestTLS_MinVersionTLS13(t *testing.T) {
	m := internalManager(t, "TLS1.3")
	srv := startTLSServer(t, m, "ok")

	client := caTrustClient(m, tls.VersionTLS12, tls.VersionTLS13)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("HTTPS GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.TLS.Version != tls.VersionTLS13 {
		t.Errorf("negotiated TLS version = %x, want TLS 1.3 (%x)", resp.TLS.Version, tls.VersionTLS13)
	}
}
