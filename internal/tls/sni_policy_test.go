package vtls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

func sniHandshake(cfg *tls.Config, sni string) error {
	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	defer func() { _ = c2.Close() }()
	errc := make(chan error, 1)
	go func() { errc <- tls.Server(c1, cfg).Handshake() }()
	cl := tls.Client(c2, &tls.Config{ServerName: sni, InsecureSkipVerify: true}) //nolint:gosec // test-only
	_ = cl.SetDeadline(time.Now().Add(10 * time.Second))
	cerr := cl.Handshake()
	_ = c2.Close()
	if serr := <-errc; serr != nil {
		return serr
	}
	return cerr
}

func certFiles(t *testing.T, dir string) int {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	n := 0
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".cert") {
			n++
		}
	}
	return n
}

// Audit F-16: with no host allow-list (path-only routes), anonymous SNI
// cycling must not grow the persistent store without bound. Before the fix
// every distinct SNI wrote a new store file.
func TestInternalCA_UnrestrictedSNIDoesNotGrowStoreUnbounded(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(ManagerConfig{Provider: "internal", StorePath: dir, StoreKey: []byte("k")})
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.TLSConfig()
	for i := 0; i < maxPersistedLeaves+40; i++ {
		if err := sniHandshake(cfg, fmt.Sprintf("n%d.invalid", i)); err != nil {
			t.Fatalf("handshake %d: %v", i, err)
		}
	}
	if n := certFiles(t, dir); n > maxPersistedLeaves+1 { // +1 for the CA
		t.Fatalf("store grew to %d files, want <= %d", n, maxPersistedLeaves+1)
	}
}

// Audit F-16: with configured hosts, only those names (and wildcard matches)
// get a certificate; anything else fails the handshake without writing state.
func TestInternalCA_HostAllowList(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(ManagerConfig{Provider: "internal", StorePath: dir, StoreKey: []byte("k"),
		Hosts: []string{"app.example.com", "*.svc.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.TLSConfig()
	for _, ok := range []string{"app.example.com", "APP.Example.com.", "a.svc.example.com", "localhost"} {
		if err := sniHandshake(cfg, ok); err != nil {
			t.Errorf("allowed SNI %q failed: %v", ok, err)
		}
	}
	before := certFiles(t, dir)
	for _, bad := range []string{"evil.example.net", "svc.example.com", "example.com"} {
		if err := sniHandshake(cfg, bad); err == nil {
			t.Errorf("unconfigured SNI %q got a certificate", bad)
		}
	}
	if after := certFiles(t, dir); after != before {
		t.Errorf("rejected names wrote %d store files", after-before)
	}
}

// Audit F-16: case and Unicode-compatibility variants of one cached ACME
// domain map to one store entry. Before the fix each variant was a new file.
func TestACME_SNIVariantsShareOneStoreEntry(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(ManagerConfig{Provider: "letsencrypt", StorePath: dir, StoreKey: []byte("k"),
		ACME: ACMEConfig{Email: "a@example.invalid", DirectoryURL: "https://127.0.0.1:1/dir"}})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example.com"},
		DNSNames: []string{"example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(80 * 24 * time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	kb, _ := x509.MarshalECPrivateKey(key)
	buf := append(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	if err := autocert.DirCache(filepath.Join(dir, "acme-cache")).Put(context.Background(), "example.com", buf); err != nil {
		t.Fatal(err)
	}
	cfg := m.TLSConfig()
	for _, s := range []string{"example.com", "EXAMPLE.com", "eXample.com", "ｅxample.com"} {
		if err := sniHandshake(cfg, s); err != nil {
			t.Fatalf("handshake %q: %v", s, err)
		}
	}
	if n := certFiles(t, dir); n != 1 {
		t.Errorf("variants produced %d store files, want 1", n)
	}
}

// Audit NV-04: with configured hosts, an unlisted SNI must not make Gubern
// place a CA order on the operator's ACME account.
func TestACME_HostPolicyBlocksOrdersForUnlistedNames(t *testing.T) {
	var orders atomic.Int64
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Replay-Nonce", fmt.Sprintf("n%d", time.Now().UnixNano()))
		switch r.URL.Path {
		case "/dir":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"newNonce": srv.URL + "/nonce", "newAccount": srv.URL + "/acct",
				"newOrder": srv.URL + "/order", "revokeCert": srv.URL + "/revoke", "keyChange": srv.URL + "/kc",
			})
		case "/acct":
			w.Header().Set("Location", srv.URL+"/acct/1")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"valid"}`))
		case "/order":
			orders.Add(1)
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"urn:ietf:params:acme:error:rejectedIdentifier"}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	m, err := NewManager(ManagerConfig{Provider: "letsencrypt", StorePath: t.TempDir(), StoreKey: make([]byte, 32),
		Hosts: []string{"app.example.com"},
		ACME:  ACMEConfig{Email: "ops@example.invalid", DirectoryURL: srv.URL + "/dir"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.TLSConfig()
	for _, n := range []string{"attacker-a.example.net", "random-1234.example.org"} {
		_ = sniHandshake(cfg, n)
	}
	if got := orders.Load(); got != 0 {
		t.Fatalf("unlisted SNIs caused %d ACME orders, want 0", got)
	}
	_ = sniHandshake(cfg, "app.example.com") // a listed host may order
	if got := orders.Load(); got != 1 {
		t.Errorf("listed host orders = %d, want 1", got)
	}
}
