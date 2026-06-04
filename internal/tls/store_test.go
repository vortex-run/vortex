package vtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeCert generates a self-signed leaf certificate valid for the given
// duration, returned as a *tls.Certificate.
func makeCert(t *testing.T, cn string, validFor time.Duration) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(validFor),
		DNSNames:     []string{cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "certs"), []byte("test-key-material"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestStore_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "certs")
	if _, err := NewStore(dir, []byte("k")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("store directory not created: err=%v", err)
	}
}

func TestStore_EmptyKeyError(t *testing.T) {
	if _, err := NewStore(t.TempDir(), nil); err == nil {
		t.Error("expected error for empty key")
	}
}

func TestStore_SaveLoadRoundTrip(t *testing.T) {
	s := newStore(t)
	orig := makeCert(t, "example.com", 90*24*time.Hour)
	if err := s.Save("example.com", orig); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := s.Load("example.com")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Certificate) != 1 {
		t.Fatalf("loaded cert blocks = %d, want 1", len(loaded.Certificate))
	}
	if string(loaded.Certificate[0]) != string(orig.Certificate[0]) {
		t.Error("loaded leaf DER differs from saved")
	}
	if loaded.Leaf == nil || loaded.Leaf.Subject.CommonName != "example.com" {
		t.Error("loaded cert leaf not parsed correctly")
	}
}

func TestStore_LoadNotExist(t *testing.T) {
	s := newStore(t)
	if _, err := s.Load("missing.com"); !os.IsNotExist(err) {
		t.Errorf("Load unknown domain err = %v, want os.ErrNotExist", err)
	}
}

func TestStore_FileIsEncrypted(t *testing.T) {
	s := newStore(t)
	cert := makeCert(t, "secret.com", time.Hour)
	if err := s.Save("secret.com", cert); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.fileFor("secret.com"))
	if err != nil {
		t.Fatal(err)
	}
	// The raw file must not contain the plaintext DER of the cert.
	if strings.Contains(string(raw), string(cert.Certificate[0])) {
		t.Error("stored file contains plaintext certificate DER (not encrypted)")
	}
	if strings.Contains(string(raw), "BEGIN CERTIFICATE") {
		t.Error("stored file contains PEM header (not encrypted)")
	}
}

func TestStore_DeleteRemoves(t *testing.T) {
	s := newStore(t)
	if err := s.Save("del.com", makeCert(t, "del.com", time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("del.com"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Load("del.com"); !os.IsNotExist(err) {
		t.Errorf("Load after Delete err = %v, want ErrNotExist", err)
	}
}

func TestStore_DeleteIdempotent(t *testing.T) {
	s := newStore(t)
	if err := s.Delete("never-existed.com"); err != nil {
		t.Errorf("Delete of missing cert should be nil, got %v", err)
	}
}

func TestStore_List(t *testing.T) {
	s := newStore(t)
	_ = s.Save("a.com", makeCert(t, "a.com", time.Hour))
	_ = s.Save("b.com", makeCert(t, "b.com", time.Hour))
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range list {
		got[d] = true
	}
	if !got["a.com"] || !got["b.com"] {
		t.Errorf("List = %v, want a.com and b.com", list)
	}
}

func TestStore_NeedsRenewalFalseForFreshCert(t *testing.T) {
	s := newStore(t)
	_ = s.Save("fresh.com", makeCert(t, "fresh.com", 60*24*time.Hour))
	need, err := s.NeedsRenewal("fresh.com", 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if need {
		t.Error("cert expiring in 60 days should not need renewal (before=30d)")
	}
}

func TestStore_NeedsRenewalTrueForExpiringCert(t *testing.T) {
	s := newStore(t)
	_ = s.Save("expiring.com", makeCert(t, "expiring.com", 10*24*time.Hour))
	need, err := s.NeedsRenewal("expiring.com", 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !need {
		t.Error("cert expiring in 10 days should need renewal (before=30d)")
	}
}

func TestStore_NeedsRenewalUnknownDomain(t *testing.T) {
	s := newStore(t)
	need, err := s.NeedsRenewal("unknown.com", 30*24*time.Hour)
	if err != nil {
		t.Errorf("unknown domain should not error, got %v", err)
	}
	if need {
		t.Error("unknown domain should not need renewal")
	}
}

func TestStore_WildcardDomain(t *testing.T) {
	s := newStore(t)
	cert := makeCert(t, "wildcard", time.Hour)
	if err := s.Save("*.example.com", cert); err != nil {
		t.Fatal(err)
	}
	// Filename must be sanitised (no literal '*').
	if strings.Contains(s.fileFor("*.example.com"), "*") {
		t.Error("wildcard filename should be sanitised")
	}
	if _, err := s.Load("*.example.com"); err != nil {
		t.Errorf("Load wildcard domain: %v", err)
	}
	list, _ := s.List()
	found := false
	for _, d := range list {
		if d == "*.example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("List should round-trip wildcard domain, got %v", list)
	}
}
