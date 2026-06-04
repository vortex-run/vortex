package vtls

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
)

// ZeroSSL ACME directory endpoint (DV, 90-day certs).
const ZeroSSLDirectoryURL = "https://acme.zerossl.com/v2/DV90"

// ManagerConfig configures the unified TLS Manager.
type ManagerConfig struct {
	// Provider selects the certificate source: "letsencrypt", "zerossl", or
	// "internal" (LocalCA).
	Provider string
	// ACME configures the ACME path (used for letsencrypt/zerossl). Email is
	// required for those providers.
	ACME ACMEConfig
	// StorePath is where certificates are stored.
	StorePath string
	// StoreKey is the encryption key for the store. Required.
	StoreKey []byte
	// MinVersion is "TLS1.2" (default) or "TLS1.3".
	MinVersion string
}

// Manager is the single TLS entry point. It wraps either a LocalCA (internal
// provider) or an ACMEManager (letsencrypt/zerossl) and exposes a hardened
// *tls.Config plus the ACME challenge handler and background renewal.
type Manager struct {
	provider string
	store    *Store
	localCA  *LocalCA
	acme     *ACMEManager
	minVer   uint16
}

// NewManager builds a Manager from cfg, creating the certificate store and the
// provider-specific backend.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	store, err := NewStore(cfg.StorePath, cfg.StoreKey)
	if err != nil {
		return nil, err
	}

	m := &Manager{provider: cfg.Provider, store: store, minVer: parseMinVersion(cfg.MinVersion)}

	switch cfg.Provider {
	case "internal":
		ca, err := NewLocalCA(store)
		if err != nil {
			return nil, err
		}
		m.localCA = ca
	case "letsencrypt", "zerossl":
		acfg := cfg.ACME
		acfg.Store = store
		if cfg.Provider == "zerossl" && acfg.DirectoryURL == "" && !acfg.Staging {
			acfg.DirectoryURL = ZeroSSLDirectoryURL
		}
		am, err := NewACMEManager(acfg)
		if err != nil {
			return nil, err
		}
		m.acme = am
	default:
		return nil, fmt.Errorf("vtls: unknown TLS provider %q", cfg.Provider)
	}
	return m, nil
}

// getCertificate routes the TLS handshake to the configured backend.
func (m *Manager) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	switch {
	case m.localCA != nil:
		name := hello.ServerName
		if name == "" {
			name = "localhost"
		}
		return m.localCA.Issue(name)
	case m.acme != nil:
		return m.acme.GetCertificate(hello)
	default:
		return nil, errors.New("vtls: no certificate provider configured")
	}
}

// TLSConfig returns a hardened *tls.Config: GetCertificate routes to the
// provider, the minimum version is enforced, and only AEAD ECDHE cipher suites
// are offered for TLS 1.2 (no RC4, 3DES, or non-ECDHE CBC). TLS 1.3 cipher
// suites are fixed by the standard library.
func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate:   m.getCertificate,
		MinVersion:       m.minVer,
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256},
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
		},
	}
}

// ChallengeHandler returns the ACME HTTP-01 challenge handler, or nil for the
// internal (LocalCA) provider which needs no challenge.
func (m *Manager) ChallengeHandler() http.Handler {
	if m.acme != nil {
		return m.acme.HTTPHandler(nil)
	}
	return nil
}

// StartBackground starts the ACME renewal loop for ACME providers; it is a
// no-op for the internal provider.
func (m *Manager) StartBackground(ctx context.Context) {
	if m.acme != nil {
		m.acme.StartRenewalLoop(ctx)
	}
}

// LocalCA returns the underlying LocalCA, or nil if the provider is not
// "internal". Useful for trust-anchor installation and tests.
func (m *Manager) LocalCA() *LocalCA { return m.localCA }

// parseMinVersion maps a config string to a tls version constant, defaulting to
// TLS 1.2.
func parseMinVersion(s string) uint16 {
	switch s {
	case "TLS1.3":
		return tls.VersionTLS13
	default:
		return tls.VersionTLS12
	}
}
