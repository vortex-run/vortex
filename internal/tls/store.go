// Package vtls implements VORTEX's TLS layer (build plan M2.4): an encrypted
// certificate store, a local development CA, an ACME (Let's Encrypt/ZeroSSL)
// manager, and a unified entry point. It is named vtls — not tls — to avoid
// shadowing the standard library's crypto/tls, which it uses heavily.
package vtls

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// certExt is the file extension for stored certificates.
const certExt = ".cert"

// Store persists TLS certificates on disk, encrypted with AES-256-GCM. Each
// domain's certificate (leaf, chain, and private key) is stored in its own
// file. The encryption key is derived from caller-supplied key material.
type Store struct {
	path string
	key  [32]byte
}

// storedCert is the JSON shape written to disk (before encryption).
type storedCert struct {
	Leaf       string    `json:"leaf"`        // base64 DER of the leaf cert
	Chain      []string  `json:"chain"`       // base64 DER of intermediate certs
	PrivateKey string    `json:"private_key"` // base64 PKCS#8 of the private key
	SavedAt    time.Time `json:"saved_at"`
}

// NewStore opens (creating if needed) a certificate store at path, deriving the
// 32-byte AES key from key (SHA-256 of key if it is not already 32 bytes).
func NewStore(path string, key []byte) (*Store, error) {
	if len(key) == 0 {
		return nil, errors.New("vtls store: encryption key must not be empty")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("creating store directory: %w", err)
	}
	s := &Store{path: path}
	if len(key) == 32 {
		copy(s.key[:], key)
	} else {
		s.key = sha256.Sum256(key)
	}
	return s, nil
}

// Save encrypts and writes cert for domain.
func (s *Store) Save(domain string, cert *tls.Certificate) error {
	if len(cert.Certificate) == 0 {
		return errors.New("vtls store: certificate has no DER blocks")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return fmt.Errorf("marshaling private key: %w", err)
	}

	sc := storedCert{
		Leaf:       base64.StdEncoding.EncodeToString(cert.Certificate[0]),
		PrivateKey: base64.StdEncoding.EncodeToString(keyDER),
		SavedAt:    time.Now().UTC(),
	}
	for _, der := range cert.Certificate[1:] {
		sc.Chain = append(sc.Chain, base64.StdEncoding.EncodeToString(der))
	}

	plain, err := json.Marshal(sc)
	if err != nil {
		return fmt.Errorf("marshaling stored cert: %w", err)
	}
	enc, err := s.encrypt(plain)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.fileFor(domain), enc, 0o600); err != nil {
		return fmt.Errorf("writing cert file: %w", err)
	}
	return nil
}

// Load decrypts and returns the certificate for domain. It returns
// os.ErrNotExist if no certificate is stored.
func (s *Store) Load(domain string) (*tls.Certificate, error) {
	enc, err := os.ReadFile(s.fileFor(domain))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("reading cert file: %w", err)
	}
	plain, err := s.decrypt(enc)
	if err != nil {
		return nil, err
	}
	var sc storedCert
	if err := json.Unmarshal(plain, &sc); err != nil {
		return nil, fmt.Errorf("unmarshaling stored cert: %w", err)
	}

	leaf, err := base64.StdEncoding.DecodeString(sc.Leaf)
	if err != nil {
		return nil, fmt.Errorf("decoding leaf: %w", err)
	}
	der := [][]byte{leaf}
	for _, c := range sc.Chain {
		b, derr := base64.StdEncoding.DecodeString(c)
		if derr != nil {
			return nil, fmt.Errorf("decoding chain: %w", derr)
		}
		der = append(der, b)
	}
	keyDER, err := base64.StdEncoding.DecodeString(sc.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("decoding private key: %w", err)
	}
	priv, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}

	cert := &tls.Certificate{Certificate: der, PrivateKey: priv}
	if parsed, perr := x509.ParseCertificate(leaf); perr == nil {
		cert.Leaf = parsed
	}
	return cert, nil
}

// List returns the domains that have stored certificates.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.path)
	if err != nil {
		return nil, fmt.Errorf("reading store directory: %w", err)
	}
	var domains []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), certExt) {
			continue
		}
		base := strings.TrimSuffix(e.Name(), certExt)
		domains = append(domains, unsanitizeDomain(base))
	}
	return domains, nil
}

// Delete removes the stored certificate for domain. It is idempotent.
func (s *Store) Delete(domain string) error {
	err := os.Remove(s.fileFor(domain))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("deleting cert file: %w", err)
	}
	return nil
}

// NeedsRenewal reports whether the stored cert for domain expires within
// `before`. A domain with no stored cert returns (false, nil): nothing to renew
// because nothing has been issued yet.
func (s *Store) NeedsRenewal(domain string, before time.Duration) (bool, error) {
	cert, err := s.Load(domain)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	leaf := cert.Leaf
	if leaf == nil {
		var perr error
		if leaf, perr = x509.ParseCertificate(cert.Certificate[0]); perr != nil {
			return false, perr
		}
	}
	return time.Until(leaf.NotAfter) < before, nil
}

// fileFor returns the on-disk path for a domain's cert file.
func (s *Store) fileFor(domain string) string {
	return filepath.Join(s.path, sanitizeDomain(domain)+certExt)
}

// sanitizeDomain makes a domain safe as a filename: "*" → "_wildcard_",
// "/" → "_".
func sanitizeDomain(domain string) string {
	domain = strings.ReplaceAll(domain, "*", "_wildcard_")
	domain = strings.ReplaceAll(domain, "/", "_")
	return domain
}

// unsanitizeDomain reverses sanitizeDomain for the wildcard marker. (The "/"
// substitution is not reversible and is not expected in real domains.)
func unsanitizeDomain(name string) string {
	return strings.ReplaceAll(name, "_wildcard_", "*")
}

// encrypt seals plaintext with AES-256-GCM, prefixing the random 12-byte nonce.
func (s *Store) encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// decrypt reverses encrypt.
func (s *Store) decrypt(data []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, errors.New("vtls store: ciphertext too short")
	}
	nonce, ct := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypting cert (wrong key or corrupt file): %w", err)
	}
	return plain, nil
}
