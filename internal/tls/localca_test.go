package vtls

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newCA(t *testing.T) *LocalCA {
	t.Helper()
	ca, err := NewLocalCA(newStore(t))
	if err != nil {
		t.Fatalf("NewLocalCA: %v", err)
	}
	return ca
}

func TestLocalCA_CreatesOnFirstCall(t *testing.T) {
	ca := newCA(t)
	if ca.CACert() == nil {
		t.Fatal("CA cert is nil")
	}
}

func TestLocalCA_LoadsExistingCA(t *testing.T) {
	store := newStore(t)
	ca1, err := NewLocalCA(store)
	if err != nil {
		t.Fatal(err)
	}
	ca2, err := NewLocalCA(store) // same store → should load, not regenerate
	if err != nil {
		t.Fatal(err)
	}
	if ca1.CACert().SerialNumber.Cmp(ca2.CACert().SerialNumber) != 0 {
		t.Error("second NewLocalCA generated a new CA instead of loading the existing one")
	}
}

func TestLocalCA_CertIsCA(t *testing.T) {
	if !newCA(t).CACert().IsCA {
		t.Error("CA cert IsCA should be true")
	}
}

func TestLocalCA_CertValidTenYears(t *testing.T) {
	ca := newCA(t)
	if ca.CACert().NotAfter.Before(time.Now().Add(9 * 365 * 24 * time.Hour)) {
		t.Errorf("CA NotAfter = %v, want > now + 9 years", ca.CACert().NotAfter)
	}
}

func TestLocalCA_IssueSignedByCA(t *testing.T) {
	ca := newCA(t)
	cert, err := ca.Issue("app.example.com")
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.CACert())
	if _, err := cert.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "app.example.com"}); err != nil {
		t.Errorf("issued cert does not verify against CA: %v", err)
	}
}

func TestLocalCA_IssuedCertSANs(t *testing.T) {
	cert, err := newCA(t).Issue("svc.example.com")
	if err != nil {
		t.Fatal(err)
	}
	leaf := cert.Leaf
	hasDNS := func(n string) bool {
		for _, d := range leaf.DNSNames {
			if d == n {
				return true
			}
		}
		return false
	}
	if !hasDNS("svc.example.com") {
		t.Error("SAN missing domain")
	}
	if !hasDNS("localhost") {
		t.Error("SAN missing localhost")
	}
	hasIP := false
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("127.0.0.1")) {
			hasIP = true
		}
	}
	if !hasIP {
		t.Error("SAN missing 127.0.0.1")
	}
}

func TestLocalCA_IssuedCertValid90Days(t *testing.T) {
	cert, err := newCA(t).Issue("x.com")
	if err != nil {
		t.Fatal(err)
	}
	d := time.Until(cert.Leaf.NotAfter)
	if d < 89*24*time.Hour || d > 91*24*time.Hour {
		t.Errorf("leaf validity = %v, want ~90 days", d)
	}
}

func TestLocalCA_IssueCachesCert(t *testing.T) {
	ca := newCA(t)
	c1, err := ca.Issue("cache.com")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := ca.Issue("cache.com")
	if err != nil {
		t.Fatal(err)
	}
	if c1.Leaf.SerialNumber.Cmp(c2.Leaf.SerialNumber) != 0 {
		t.Error("second Issue returned a new cert; expected the cached one")
	}
}

func TestLocalCA_TLSConfigHasGetCertificate(t *testing.T) {
	if newCA(t).TLSConfig().GetCertificate == nil {
		t.Error("TLSConfig().GetCertificate is nil")
	}
}

func TestLocalCA_FullHandshake(t *testing.T) {
	ca := newCA(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure ok")
	}))
	srv.TLS = ca.TLSConfig()
	srv.StartTLS()
	defer srv.Close()

	pool := x509.NewCertPool()
	pool.AddCert(ca.CACert())
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12},
	}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("HTTPS GET via LocalCA: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "secure ok" {
		t.Errorf("body = %q, want 'secure ok'", body)
	}
}
