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

// ServerTLSConfig returns a *tls.Config that requires and verifies a client
// certificate from the cluster CA with a valid SPIFFE identity.
func (m *MTLSConfig) ServerTLSConfig() *tls.Config {
	pool, _ := BuildCAPool(m.RotationMgr.ClusterCA())
	return &tls.Config{
		MinVersion: m.MinVersion,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  pool,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return m.RotationMgr.Current(), nil
		},
		VerifyPeerCertificate: m.verifyPeer,
	}
}

// ClientTLSConfig returns a *tls.Config that presents this node's certificate
// and verifies the server's certificate against the cluster CA and trust domain.
func (m *MTLSConfig) ClientTLSConfig() *tls.Config {
	pool, _ := BuildCAPool(m.RotationMgr.ClusterCA())
	return &tls.Config{
		MinVersion:         m.MinVersion,
		InsecureSkipVerify: false, //nolint:gosec // RootCAs + VerifyPeerCertificate enforce trust
		RootCAs:            pool,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return m.RotationMgr.Current(), nil
		},
		VerifyPeerCertificate: m.verifyPeer,
	}
}

// verifyPeer runs after the standard chain verification: it extracts the peer's
// SPIFFE identity and confirms its trust domain matches ours.
func (m *MTLSConfig) verifyPeer(_ [][]byte, verifiedChains [][]*x509.Certificate) error {
	if len(verifiedChains) == 0 || len(verifiedChains[0]) == 0 {
		return errors.New("vtls mtls: no verified peer certificate chain")
	}
	leaf := verifiedChains[0][0]

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
