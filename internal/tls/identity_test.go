package vtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"regexp"
	"testing"
	"time"
)

var hex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

// testClusterCA builds an ECDSA P-256 CA cert+key for signing node certs.
func testClusterCA(t *testing.T) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "VORTEX Cluster CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(der)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: caCert}
}

func TestNodeIdentity_StableNodeID(t *testing.T) {
	a, err := NewNodeIdentity("testcluster")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewNodeIdentity("testcluster")
	if err != nil {
		t.Fatal(err)
	}
	if a.NodeID != b.NodeID {
		t.Errorf("nodeID not stable: %q vs %q", a.NodeID, b.NodeID)
	}
}

func TestNodeIdentity_NodeIDIs16Hex(t *testing.T) {
	id, err := NewNodeIdentity("testcluster")
	if err != nil {
		t.Fatal(err)
	}
	if !hex16.MatchString(id.NodeID) {
		t.Errorf("nodeID %q is not 16 hex chars", id.NodeID)
	}
}

func TestNodeIdentity_SPIFFEURIFormat(t *testing.T) {
	id, err := NewNodeIdentity("testcluster")
	if err != nil {
		t.Fatal(err)
	}
	want := "spiffe://testcluster.vortex/node/" + id.NodeID
	if id.SPIFFEURI != want {
		t.Errorf("SPIFFE URI = %q, want %q", id.SPIFFEURI, want)
	}
	if id.TrustDomain != "testcluster.vortex" {
		t.Errorf("trust domain = %q, want testcluster.vortex", id.TrustDomain)
	}
}

func TestNodeIdentity_EmptyClusterFallback(t *testing.T) {
	id, err := NewNodeIdentity("")
	if err != nil {
		t.Fatal(err)
	}
	if id.TrustDomain != "vortex.local" {
		t.Errorf("trust domain = %q, want vortex.local fallback", id.TrustDomain)
	}
	if !hex16.MatchString(id.NodeID) {
		t.Errorf("nodeID %q should still derive with empty cluster", id.NodeID)
	}
}

func TestNodeIdentity_URISANs(t *testing.T) {
	id, _ := NewNodeIdentity("testcluster")
	sans := id.URISANs()
	if len(sans) != 1 {
		t.Fatalf("URISANs len = %d, want 1", len(sans))
	}
	if sans[0].Scheme != "spiffe" {
		t.Errorf("URI scheme = %q, want spiffe", sans[0].Scheme)
	}
	if sans[0].String() != id.SPIFFEURI {
		t.Errorf("URI = %q, want %q", sans[0].String(), id.SPIFFEURI)
	}
}

func TestNodeIdentity_IssueNodeCert(t *testing.T) {
	ca := testClusterCA(t)
	id, _ := NewNodeIdentity("testcluster")

	cert, err := id.IssueNodeCert(ca, 24*time.Hour)
	if err != nil {
		t.Fatalf("IssueNodeCert: %v", err)
	}
	leaf := cert.Leaf
	if leaf == nil {
		t.Fatal("issued cert has no parsed leaf")
	}

	// Verifies against the CA pool.
	pool := x509.NewCertPool()
	pool.AddCert(ca.Leaf)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("issued cert does not verify against CA: %v", err)
	}

	// SPIFFE URI present.
	uri, err := ExtractSPIFFEID(leaf)
	if err != nil || uri != id.SPIFFEURI {
		t.Errorf("ExtractSPIFFEID = %q, %v; want %q", uri, err, id.SPIFFEURI)
	}

	// Lifetime ~ now + 24h.
	wantExpiry := time.Now().Add(24 * time.Hour)
	if diff := leaf.NotAfter.Sub(wantExpiry); diff > 2*time.Minute || diff < -2*time.Minute {
		t.Errorf("NotAfter = %v, want ~%v", leaf.NotAfter, wantExpiry)
	}

	// Dual ExtKeyUsage.
	var hasClient, hasServer bool
	for _, eku := range leaf.ExtKeyUsage {
		switch eku {
		case x509.ExtKeyUsageClientAuth:
			hasClient = true
		case x509.ExtKeyUsageServerAuth:
			hasServer = true
		}
	}
	if !hasClient || !hasServer {
		t.Errorf("ExtKeyUsage missing client(%v)/server(%v) auth", hasClient, hasServer)
	}

	// Not a CA.
	if leaf.IsCA {
		t.Error("node cert must not be a CA")
	}
}

func TestExtractSPIFFEID_FromIssuedCert(t *testing.T) {
	ca := testClusterCA(t)
	id, _ := NewNodeIdentity("testcluster")
	cert, _ := id.IssueNodeCert(ca, time.Hour)
	got, err := ExtractSPIFFEID(cert.Leaf)
	if err != nil {
		t.Fatal(err)
	}
	if got != id.SPIFFEURI {
		t.Errorf("got %q, want %q", got, id.SPIFFEURI)
	}
}

func TestExtractSPIFFEID_NoURISAN(t *testing.T) {
	// A cert with no URI SANs.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "no-uri"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	if _, err := ExtractSPIFFEID(cert); err == nil {
		t.Error("expected error for cert with no SPIFFE URI SAN")
	}
}

func TestValidateSPIFFEID_CorrectDomain(t *testing.T) {
	if err := ValidateSPIFFEID("spiffe://prod.vortex/node/abc123", "prod.vortex"); err != nil {
		t.Errorf("expected valid, got %v", err)
	}
}

func TestValidateSPIFFEID_WrongDomain(t *testing.T) {
	if err := ValidateSPIFFEID("spiffe://staging.vortex/node/abc123", "prod.vortex"); err == nil {
		t.Error("expected error for wrong trust domain")
	}
}

func TestValidateSPIFFEID_NonSpiffeScheme(t *testing.T) {
	if err := ValidateSPIFFEID("https://prod.vortex/node/abc123", "prod.vortex"); err == nil {
		t.Error("expected error for non-spiffe scheme")
	}
}
