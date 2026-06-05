package vtls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
)

// MTLSConfig builds mutual-TLS server and client configurations for the
// identity mesh. Both sides require and verify a peer certificate that chains to
// the cluster CA and carries a SPIFFE URI in the expected trust domain. The
// local certificate is read from the RotationManager on every handshake via
// GetCertificate/GetClientCertificate, so a rotated cert is picked up
// atomically without restarting listeners.
type MTLSConfig struct {
	RotationMgr *RotationManager
	TrustDomain string
	MinVersion  uint16
	Logger      *slog.Logger
}

// NewMTLSConfig validates and returns an MTLSConfig with defaults applied.
func NewMTLSConfig(cfg MTLSConfig) (*MTLSConfig, error) {
	if cfg.RotationMgr == nil {
		return nil, errors.New("vtls mtls: RotationManager is required")
	}
	if cfg.TrustDomain == "" {
		return nil, errors.New("vtls mtls: TrustDomain is required")
	}
	if cfg.MinVersion == 0 {
		cfg.MinVersion = tls.VersionTLS13
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &cfg, nil
}

// ServerTLSConfig returns a *tls.Config that requires a client certificate and
// verifies it (chain + SPIFFE identity) via verifyPeer.
func (m *MTLSConfig) ServerTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: m.MinVersion,
		// RequestAnyClientCert + manual verification: we authorize peers by
		// their SPIFFE identity (not hostname), so we run our own chain check in
		// VerifyPeerCertificate rather than relying on the default verifier.
		ClientAuth: tls.RequireAnyClientCert,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return m.RotationMgr.Current(), nil
		},
		VerifyPeerCertificate: m.verifyPeer,
	}
}

// ClientTLSConfig returns a *tls.Config that presents this node's certificate
// and verifies the server by SPIFFE identity. Hostname verification is disabled
// (InsecureSkipVerify) because nodes are identified by SPIFFE URI, not DNS name;
// full chain + identity verification is enforced in verifyPeer instead.
func (m *MTLSConfig) ClientTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:         m.MinVersion,
		InsecureSkipVerify: true, //nolint:gosec // verifyPeer performs full chain + SPIFFE verification
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return m.RotationMgr.Current(), nil
		},
		VerifyPeerCertificate: m.verifyPeer,
	}
}

// verifyPeer manually verifies the peer's certificate chain against the cluster
// CA and confirms its SPIFFE identity is in our trust domain. It is used in
// place of the default verifier so peers are authorized by SPIFFE identity
// rather than DNS name.
func (m *MTLSConfig) verifyPeer(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return errors.New("vtls mtls: peer presented no certificate")
	}

	certs := make([]*x509.Certificate, 0, len(rawCerts))
	for _, der := range rawCerts {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return fmt.Errorf("vtls mtls: parsing peer cert: %w", err)
		}
		certs = append(certs, c)
	}
	leaf := certs[0]

	// Verify the chain against the cluster CA.
	pool, err := BuildCAPool(m.RotationMgr.ClusterCA())
	if err != nil {
		return err
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: inter,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return fmt.Errorf("vtls mtls: peer chain verification failed: %w", err)
	}

	// Authorize by SPIFFE identity in our trust domain.
	spiffeID, err := ExtractSPIFFEID(leaf)
	if err != nil {
		return fmt.Errorf("vtls mtls: peer cert lacks SPIFFE identity: %w", err)
	}
	if err := ValidateSPIFFEID(spiffeID, m.TrustDomain); err != nil {
		return fmt.Errorf("vtls mtls: peer SPIFFE identity rejected: %w", err)
	}

	m.Logger.Debug("mTLS peer verified", "peer_spiffe_id", spiffeID)
	return nil
}

// BuildCAPool returns an x509.CertPool containing the cluster CA certificate.
func BuildCAPool(caCert *tls.Certificate) (*x509.CertPool, error) {
	if caCert == nil || len(caCert.Certificate) == 0 {
		return nil, errors.New("vtls mtls: CA certificate is nil or empty")
	}
	leaf := caCert.Leaf
	if leaf == nil {
		parsed, err := x509.ParseCertificate(caCert.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("vtls mtls: parsing CA cert: %w", err)
		}
		leaf = parsed
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return pool, nil
}
