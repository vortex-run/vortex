package vtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

// caStoreDomain is the pseudo-domain under which the CA cert+key is persisted.
const caStoreDomain = "_ca"

// CA/leaf validity windows.
const (
	caValidity      = 10 * 365 * 24 * time.Hour // ~10 years
	leafValidity    = 90 * 24 * time.Hour       // 90 days
	leafRenewBefore = 30 * 24 * time.Hour
)

// LocalCA is a self-signed certificate authority for dev/staging use
// (config tls.provider = "internal"). It issues short-lived leaf certificates
// signed by a long-lived CA, persisting both in the encrypted Store.
type LocalCA struct {
	store   *Store
	caCert  *x509.Certificate
	caKey   *ecdsa.PrivateKey
	caChain [][]byte // CA cert DER, attached to issued certs as the chain
}

// NewLocalCA loads an existing CA from store or generates a new one and
// persists it.
func NewLocalCA(store *Store) (*LocalCA, error) {
	if store == nil {
		return nil, errors.New("vtls localca: store is required")
	}
	ca := &LocalCA{store: store}

	if cert, err := store.Load(caStoreDomain); err == nil {
		key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("vtls localca: stored CA key is not ECDSA")
		}
		x509Cert := cert.Leaf
		if x509Cert == nil {
			if x509Cert, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
				return nil, fmt.Errorf("parsing stored CA cert: %w", err)
			}
		}
		ca.caCert = x509Cert
		ca.caKey = key
		ca.caChain = [][]byte{cert.Certificate[0]}
		return ca, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("loading CA: %w", err)
	}

	if err := ca.generate(); err != nil {
		return nil, err
	}
	return ca, nil
}

// generate creates a fresh self-signed CA and saves it to the store.
func (ca *LocalCA) generate() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating CA key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "VORTEX Local CA", Organization: []string{"VORTEX Dev"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caValidity),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
		MaxPathLenZero:        false,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("creating CA cert: %w", err)
	}
	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		return fmt.Errorf("parsing CA cert: %w", err)
	}

	ca.caCert = caCert
	ca.caKey = key
	ca.caChain = [][]byte{der}

	tlsCert := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: caCert}
	if err := ca.store.Save(caStoreDomain, tlsCert); err != nil {
		return fmt.Errorf("saving CA: %w", err)
	}
	return nil
}

// Issue returns a leaf certificate for domain signed by the CA. If a valid,
// non-expiring cert is already stored for the domain it is returned from cache;
// otherwise a new 90-day cert is issued and persisted.
func (ca *LocalCA) Issue(domain string) (*tls.Certificate, error) {
	if need, err := ca.store.NeedsRenewal(domain, leafRenewBefore); err == nil && !need {
		if cert, lerr := ca.store.Load(domain); lerr == nil {
			return cert, nil
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating leaf key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	addSANs(tmpl, domain)

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.caCert, &key.PublicKey, ca.caKey)
	if err != nil {
		return nil, fmt.Errorf("signing leaf cert: %w", err)
	}
	leaf, _ := x509.ParseCertificate(der)

	cert := &tls.Certificate{
		Certificate: append([][]byte{der}, ca.caChain...),
		PrivateKey:  key,
		Leaf:        leaf,
	}
	if err := ca.store.Save(domain, cert); err != nil {
		return nil, fmt.Errorf("saving issued cert: %w", err)
	}
	return cert, nil
}

// addSANs adds the domain plus localhost loopback SANs. If domain is an IP it is
// added as an IP SAN rather than a DNS SAN.
func addSANs(tmpl *x509.Certificate, domain string) {
	if ip := net.ParseIP(domain); ip != nil {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	} else {
		tmpl.DNSNames = append(tmpl.DNSNames, domain)
	}
	tmpl.DNSNames = append(tmpl.DNSNames, "localhost")
	tmpl.IPAddresses = append(tmpl.IPAddresses, net.ParseIP("127.0.0.1"), net.ParseIP("::1"))
}

// CACert returns the CA certificate, for installing as a trust anchor.
func (ca *LocalCA) CACert() *x509.Certificate { return ca.caCert }

// TLSConfig returns a *tls.Config whose GetCertificate issues (or returns a
// cached) leaf certificate for the requested ServerName via the CA.
func (ca *LocalCA) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name = "localhost"
			}
			return ca.Issue(name)
		},
	}
}

// randSerial returns a random 128-bit positive serial number.
func randSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generating serial: %w", err)
	}
	return serial, nil
}
