package vtls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// Store domains for the cluster CA and this node's cert.
const (
	clusterCADomain = "_cluster_ca"
	nodeCertDomain  = "_node_cert"
)

// Rotation defaults.
const (
	defaultCertLifetime = 24 * time.Hour
	defaultRotateAt     = 4 * time.Hour
	clusterCAValidity   = 10 * 365 * 24 * time.Hour // ~10 years
	maxCheckInterval    = 30 * time.Minute
)

// RotationConfig configures a RotationManager.
type RotationConfig struct {
	// ClusterName scopes the trust domain and CA subject. Required.
	ClusterName string
	// Store persists the cluster CA and node cert (the M2.4 vtls.Store).
	Store *Store
	// CertLifetime is the validity of issued node certs. Default 24h.
	CertLifetime time.Duration
	// RotateAt is the remaining-lifetime threshold that triggers rotation.
	// Default 4h.
	RotateAt time.Duration
	// Logger receives rotation events; defaults to slog.Default.
	Logger *slog.Logger
}

// RotationManager owns the cluster CA and rotates this node's mTLS identity
// certificate. The current cert is held behind an atomic pointer so it can be
// swapped under live traffic without restarting listeners: the mTLS config's
// GetCertificate/GetClientCertificate callbacks read Current() on each
// handshake, so established connections are unaffected and in-flight handshakes
// always get a valid cert.
type RotationManager struct {
	cfg       RotationConfig
	log       *slog.Logger
	identity  *NodeIdentity
	clusterCA *tls.Certificate

	current atomic.Pointer[tls.Certificate]
}

// NewRotationManager loads or generates the cluster CA, loads or issues this
// node's cert, and returns a ready manager. It does not start the rotation loop
// — call StartRotation for that.
func NewRotationManager(cfg RotationConfig) (*RotationManager, error) {
	if cfg.Store == nil {
		return nil, errors.New("vtls rotation: Store is required")
	}
	if cfg.ClusterName == "" {
		return nil, errors.New("vtls rotation: ClusterName is required")
	}
	if cfg.CertLifetime <= 0 {
		cfg.CertLifetime = defaultCertLifetime
	}
	if cfg.RotateAt <= 0 {
		cfg.RotateAt = defaultRotateAt
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	identity, err := NewNodeIdentity(cfg.ClusterName)
	if err != nil {
		return nil, err
	}

	r := &RotationManager{cfg: cfg, log: cfg.Logger, identity: identity}

	if err := r.loadOrGenerateCA(); err != nil {
		return nil, err
	}
	cert, err := r.loadOrIssueNodeCert()
	if err != nil {
		return nil, err
	}
	r.current.Store(cert)
	return r, nil
}

// loadOrGenerateCA loads the cluster CA from the store, generating and
// persisting a new one if absent.
func (r *RotationManager) loadOrGenerateCA() error {
	if cert, err := r.cfg.Store.Load(clusterCADomain); err == nil {
		if cert.Leaf == nil {
			if leaf, perr := x509.ParseCertificate(cert.Certificate[0]); perr == nil {
				cert.Leaf = leaf
			}
		}
		r.clusterCA = cert
		return nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("vtls rotation: generating cluster CA key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "VORTEX Cluster CA", Organization: []string{r.cfg.ClusterName}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(clusterCAValidity),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("vtls rotation: creating cluster CA: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return fmt.Errorf("vtls rotation: parsing cluster CA: %w", err)
	}

	ca := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	if err := r.cfg.Store.Save(clusterCADomain, ca); err != nil {
		return fmt.Errorf("vtls rotation: saving cluster CA: %w", err)
	}
	r.clusterCA = ca
	return nil
}

// loadOrIssueNodeCert returns a cached node cert if present and not expiring,
// otherwise issues, persists, and returns a fresh one.
func (r *RotationManager) loadOrIssueNodeCert() (*tls.Certificate, error) {
	if need, err := r.cfg.Store.NeedsRenewal(nodeCertDomain, r.cfg.RotateAt); err == nil && !need {
		if cert, lerr := r.cfg.Store.Load(nodeCertDomain); lerr == nil {
			ensureLeaf(cert)
			return cert, nil
		}
	}
	return r.issueAndStore()
}

// issueAndStore issues a fresh node cert from the cluster CA and persists it.
func (r *RotationManager) issueAndStore() (*tls.Certificate, error) {
	cert, err := r.identity.IssueNodeCert(r.clusterCA, r.cfg.CertLifetime)
	if err != nil {
		return nil, err
	}
	if err := r.cfg.Store.Save(nodeCertDomain, cert); err != nil {
		return nil, fmt.Errorf("vtls rotation: saving node cert: %w", err)
	}
	return cert, nil
}

// Current returns the active node certificate. It is never nil after
// NewRotationManager succeeds.
func (r *RotationManager) Current() *tls.Certificate { return r.current.Load() }

// ClusterCA returns the cluster CA certificate, for building trust pools.
func (r *RotationManager) ClusterCA() *tls.Certificate { return r.clusterCA }

// Identity returns this node's SPIFFE identity.
func (r *RotationManager) Identity() *NodeIdentity { return r.identity }

// StartRotation runs a background goroutine that re-issues the node cert before
// it expires. It checks periodically (every min(30m, RotateAt/2)); when the
// current cert's remaining lifetime drops below RotateAt it issues a new cert,
// persists it, and atomically swaps it in. Issue failures are logged and
// retried on the next check — the loop never panics and stops only on ctx
// cancellation.
func (r *RotationManager) StartRotation(ctx context.Context) {
	go func() {
		interval := maxCheckInterval
		if half := r.cfg.RotateAt / 2; half > 0 && half < interval {
			interval = half
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.rotateIfNeeded()
			}
		}
	}()
}

// rotateIfNeeded swaps in a fresh cert when the current one nears expiry.
func (r *RotationManager) rotateIfNeeded() {
	cur := r.current.Load()
	if cur == nil || cur.Leaf == nil {
		return
	}
	if time.Until(cur.Leaf.NotAfter) >= r.cfg.RotateAt {
		return // still fresh
	}

	newCert, err := r.issueAndStore()
	if err != nil {
		r.log.Error("node cert rotation failed; keeping current cert",
			"node_id", r.identity.NodeID, "err", err)
		return
	}
	r.current.Store(newCert)
	r.log.Info("node cert rotated",
		"node_id", r.identity.NodeID,
		"expires_at", newCert.Leaf.NotAfter.Format(time.RFC3339),
	)
}

// ensureLeaf parses and attaches the leaf certificate if it is missing.
func ensureLeaf(cert *tls.Certificate) {
	if cert.Leaf == nil && len(cert.Certificate) > 0 {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
			cert.Leaf = leaf
		}
	}
}
