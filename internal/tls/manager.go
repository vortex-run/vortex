package vtls

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// ticketRotationInterval is how often session ticket keys are rotated (M19).
// The previous key is kept alongside the new one so sessions resumed across a
// rotation boundary still work.
const ticketRotationInterval = 24 * time.Hour

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

	// issued tracks every *tls.Config handed out by TLSConfig so the ticket
	// rotation loop started by StartBackground can re-key all of them.
	mu       sync.Mutex
	issued   []*tls.Config
	ocspOnce sync.Once
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
// provider, the minimum version is enforced, and only forward-secret AEAD
// ECDHE cipher suites are offered for TLS 1.2 (no TLS_RSA_*, RC4, 3DES, or
// non-ECDHE CBC). TLS 1.3 cipher suites are fixed by the standard library.
// Every returned config is registered for 24h session ticket key rotation
// (see StartBackground).
func (m *Manager) TLSConfig() *tls.Config {
	// OCSP stapling is not implemented yet: Go's crypto/tls serves a staple
	// only when the certificate carries one, and neither backend fetches OCSP
	// responses. Warn once so operators know revocation checking falls back to
	// client-side behaviour.
	m.ocspOnce.Do(func() {
		slog.Default().Warn("OCSP stapling not available; certificates are served without stapled OCSP responses")
	})
	cfg := &tls.Config{
		GetCertificate:   m.getCertificate,
		MinVersion:       m.minVer,
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256},
		CipherSuites: []uint16{
			// AES-128-GCM first: Go's HTTP/2 stack requires at least one of the
			// AES_128_GCM_SHA256 ECDHE suites or it refuses the listener.
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
		},
	}
	m.mu.Lock()
	m.issued = append(m.issued, cfg)
	m.mu.Unlock()
	return cfg
}

// rotateTicketKeys installs fresh session ticket keys on every issued config,
// keeping the previous key valid so resumption survives the rotation boundary.
// current/previous are owned by the rotation loop in StartBackground.
func (m *Manager) rotateTicketKeys(current, previous *[32]byte) {
	*previous = *current
	if _, err := rand.Read(current[:]); err != nil {
		slog.Default().Warn("session ticket key rotation failed; keeping previous keys", "err", err)
		return
	}
	keys := [][32]byte{*current, *previous}

	m.mu.Lock()
	configs := make([]*tls.Config, len(m.issued))
	copy(configs, m.issued)
	m.mu.Unlock()

	for _, cfg := range configs {
		cfg.SetSessionTicketKeys(keys)
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

// StartBackground starts the manager's background loops: ACME renewal (ACME
// providers only) and 24h session ticket key rotation for every config issued
// by TLSConfig. Non-blocking; both loops stop when ctx is cancelled.
func (m *Manager) StartBackground(ctx context.Context) {
	if m.acme != nil {
		m.acme.StartRenewalLoop(ctx)
	}
	go func() {
		var current, previous [32]byte
		if _, err := rand.Read(current[:]); err != nil {
			slog.Default().Warn("seeding session ticket keys failed; using Go's automatic rotation", "err", err)
			return
		}
		previous = current
		m.rotateTicketKeys(&current, &previous)

		ticker := time.NewTicker(ticketRotationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.rotateTicketKeys(&current, &previous)
			}
		}
	}()
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
