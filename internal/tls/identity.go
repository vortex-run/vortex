package vtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// fallbackTrustDomain is used when the cluster name is empty.
const fallbackTrustDomain = "vortex.local"

// spiffeScheme is the URI scheme for SPIFFE identities.
const spiffeScheme = "spiffe"

// NodeIdentity is a VORTEX node's SPIFFE-style identity used for the mTLS
// identity mesh. The node ID is derived deterministically from the cluster name
// and hostname so it is stable across restarts, and the trust domain is
// cluster-scoped so a cert from one cluster cannot authenticate to another.
type NodeIdentity struct {
	NodeID      string // 16-char hex, SHA-256 derived
	SPIFFEURI   string // spiffe://<trust-domain>/node/<id>
	TrustDomain string // <cluster-name>.vortex (or vortex.local)
}

// NewNodeIdentity derives this node's identity from the cluster name and the
// host's name. The node ID is the first 16 hex characters of
// SHA-256(clusterName + "/" + hostname); the trust domain is
// clusterName + ".vortex" (or "vortex.local" when clusterName is empty).
func NewNodeIdentity(clusterName string) (*NodeIdentity, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("vtls identity: resolving hostname: %w", err)
	}

	sum := sha256.Sum256([]byte(clusterName + "/" + hostname))
	nodeID := hex.EncodeToString(sum[:])[:16]

	trustDomain := clusterName + ".vortex"
	if clusterName == "" {
		trustDomain = fallbackTrustDomain
	}

	return &NodeIdentity{
		NodeID:      nodeID,
		TrustDomain: trustDomain,
		SPIFFEURI:   spiffeScheme + "://" + trustDomain + "/node/" + nodeID,
	}, nil
}

// URISANs returns the SPIFFE URI as a parsed *url.URL slice for the x509
// certificate URIs field.
func (n *NodeIdentity) URISANs() []*url.URL {
	u, err := url.Parse(n.SPIFFEURI)
	if err != nil {
		return nil
	}
	return []*url.URL{u}
}

// IssueNodeCert issues an ECDSA P-256 node certificate carrying this identity's
// SPIFFE URI SAN, signed by the cluster CA. The CA must carry both its parsed
// leaf certificate (or a parseable DER) and its private key. The issued cert is
// usable for both client and server auth (mTLS peers act as both).
func (n *NodeIdentity) IssueNodeCert(ca *tls.Certificate, lifetime time.Duration) (*tls.Certificate, error) {
	caCert, caKey, err := caMaterial(ca)
	if err != nil {
		return nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("vtls identity: generating node key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: n.NodeID, Organization: []string{"VORTEX Cluster"}},
		URIs:         n.URISANs(),
		DNSNames:     []string{n.NodeID}, // convenience for debugging
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(lifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		IsCA:         false,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("vtls identity: signing node cert: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("vtls identity: parsing node cert: %w", err)
	}

	return &tls.Certificate{
		Certificate: append([][]byte{der}, ca.Certificate...),
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// caMaterial extracts the parsed CA certificate and its signing key from a
// *tls.Certificate.
func caMaterial(ca *tls.Certificate) (*x509.Certificate, any, error) {
	if ca == nil || len(ca.Certificate) == 0 {
		return nil, nil, errors.New("vtls identity: CA certificate is empty")
	}
	caCert := ca.Leaf
	if caCert == nil {
		parsed, err := x509.ParseCertificate(ca.Certificate[0])
		if err != nil {
			return nil, nil, fmt.Errorf("vtls identity: parsing CA cert: %w", err)
		}
		caCert = parsed
	}
	if ca.PrivateKey == nil {
		return nil, nil, errors.New("vtls identity: CA certificate has no private key")
	}
	return caCert, ca.PrivateKey, nil
}

// ExtractSPIFFEID returns the SPIFFE URI carried in cert's URI SANs. It errors
// if the certificate has no SPIFFE URI.
func ExtractSPIFFEID(cert *x509.Certificate) (string, error) {
	for _, u := range cert.URIs {
		if u.Scheme == spiffeScheme {
			return u.String(), nil
		}
	}
	return "", errors.New("vtls identity: certificate has no SPIFFE URI SAN")
}

// ValidateSPIFFEID checks that spiffeURI is a well-formed
// spiffe://<domain>/node/<id> identity whose trust domain matches
// expectedTrustDomain.
func ValidateSPIFFEID(spiffeURI, expectedTrustDomain string) error {
	u, err := url.Parse(spiffeURI)
	if err != nil {
		return fmt.Errorf("vtls identity: parsing SPIFFE URI: %w", err)
	}
	if u.Scheme != spiffeScheme {
		return fmt.Errorf("vtls identity: URI scheme %q is not %q", u.Scheme, spiffeScheme)
	}
	if u.Host != expectedTrustDomain {
		return fmt.Errorf("vtls identity: trust domain %q does not match expected %q", u.Host, expectedTrustDomain)
	}
	if !strings.HasPrefix(u.Path, "/node/") || len(strings.TrimPrefix(u.Path, "/node/")) == 0 {
		return fmt.Errorf("vtls identity: SPIFFE path %q is not /node/<id>", u.Path)
	}
	return nil
}
