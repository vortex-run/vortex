package vtls

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// ACME directory endpoints.
const (
	LetsEncryptProductionURL = "https://acme-v02.api.letsencrypt.org/directory"
	LetsEncryptStagingURL    = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// defaultRenewBefore is the default lead time before expiry to renew.
const defaultRenewBefore = 30 * 24 * time.Hour

// ACMEConfig configures an ACMEManager.
type ACMEConfig struct {
	// Email is the ACME account contact. Required.
	Email string
	// DirectoryURL is the ACME directory endpoint. Defaults to Let's Encrypt
	// production (or staging when Staging is true).
	DirectoryURL string
	// Store persists issued certificates (in addition to autocert's own cache).
	Store *Store
	// RenewBefore is the lead time before expiry to renew. Default 30 days.
	RenewBefore time.Duration
	// Staging uses the Let's Encrypt staging environment.
	Staging bool
	// Logger receives renewal diagnostics; defaults to slog.Default.
	Logger *slog.Logger
}

// ACMEManager obtains and renews certificates via the ACME protocol, backed by
// golang.org/x/crypto/acme/autocert. Issued certificates are also mirrored into
// the VORTEX encrypted Store so the rest of the system has a single source of
// truth and can drive renewal scheduling.
type ACMEManager struct {
	cfg         ACMEConfig
	log         *slog.Logger
	mgr         *autocert.Manager
	renewBefore time.Duration
}

// NewACMEManager validates cfg and constructs an ACMEManager.
func NewACMEManager(cfg ACMEConfig) (*ACMEManager, error) {
	if cfg.Email == "" {
		return nil, errors.New("vtls acme: Email is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("vtls acme: Store is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	dir := cfg.DirectoryURL
	if cfg.Staging {
		dir = LetsEncryptStagingURL
	} else if dir == "" {
		dir = LetsEncryptProductionURL
	}

	renewBefore := cfg.RenewBefore
	if renewBefore <= 0 {
		renewBefore = defaultRenewBefore
	}

	m := &ACMEManager{cfg: cfg, log: cfg.Logger, renewBefore: renewBefore}
	m.mgr = &autocert.Manager{
		Prompt:      autocert.AcceptTOS,
		Email:       cfg.Email,
		Cache:       autocert.DirCache(filepath.Join(cfg.Store.path, "acme-cache")),
		Client:      &acme.Client{DirectoryURL: dir},
		RenewBefore: renewBefore,
	}
	return m, nil
}

// GetCertificate is the tls.Config.GetCertificate callback. It first serves a
// valid cached cert from the Store, then falls back to autocert (which performs
// ACME issuance), mirroring any newly obtained cert into the Store.
func (m *ACMEManager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	domain := hello.ServerName

	if domain != "" {
		if need, err := m.cfg.Store.NeedsRenewal(domain, m.renewBefore); err == nil && !need {
			if cert, lerr := m.cfg.Store.Load(domain); lerr == nil {
				return cert, nil
			}
		}
	}

	cert, err := m.mgr.GetCertificate(hello)
	if err != nil {
		return nil, err
	}
	if domain != "" {
		if serr := m.cfg.Store.Save(domain, cert); serr != nil {
			m.log.Warn("failed to mirror ACME cert to store", "domain", domain, "err", serr)
		}
	}
	return cert, nil
}

// HTTPHandler returns the HTTP-01 challenge handler. It must be mounted on
// port 80 so the ACME CA can validate domain control. Non-challenge requests
// fall through to fallback (or 404 if fallback is nil).
func (m *ACMEManager) HTTPHandler(fallback http.Handler) http.Handler {
	return m.mgr.HTTPHandler(fallback)
}

// StartRenewalLoop runs a background goroutine that, every 12 hours, renews any
// stored certificate within RenewBefore of expiry. It never panics; all errors
// are logged and the loop continues until ctx is cancelled.
func (m *ACMEManager) StartRenewalLoop(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(12 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.renewAll()
			}
		}
	}()
}

// renewAll renews every stored cert that is within RenewBefore of expiry by
// re-invoking GetCertificate (which obtains and re-stores a fresh cert).
func (m *ACMEManager) renewAll() {
	domains, err := m.cfg.Store.List()
	if err != nil {
		m.log.Error("renewal: listing stored certs failed", "err", err)
		return
	}
	for _, domain := range domains {
		if domain == caStoreDomain {
			continue
		}
		need, err := m.cfg.Store.NeedsRenewal(domain, m.renewBefore)
		if err != nil || !need {
			continue
		}
		if _, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: domain}); err != nil {
			m.log.Error("renewal failed", "domain", domain, "err", err)
			continue
		}
		m.log.Info("renewed cert", "domain", domain)
	}
}
