// Package secrets implements VORTEX's encrypted secret store (build plan M3.2):
// a key-value store for arbitrary secret strings (database passwords, API keys,
// JWT secrets) encrypted at rest with XChaCha20-Poly1305. It is distinct from
// the vtls cert store — that holds TLS certificates; this holds secret values
// that are injected into managed processes at runtime and never written to
// config or disk in plaintext (Non-Negotiable Rule #2).
package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// secretExt is the file extension for stored secrets.
const secretExt = ".secret"

// validName allows only alphanumerics and underscores in secret names, so a
// name is always a safe filename and a valid environment variable name.
var validName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// SecretStore persists secret values on disk, each encrypted with
// XChaCha20-Poly1305 (24-byte nonce). The encryption key is derived from
// caller-supplied key material via SHA-256.
type SecretStore struct {
	path string
	key  [chacha20poly1305.KeySize]byte
}

// NewSecretStore opens (creating if needed) a secret store at path, deriving the
// 32-byte key from key material via SHA-256.
func NewSecretStore(path string, key []byte) (*SecretStore, error) {
	if len(key) == 0 {
		return nil, errors.New("secrets: encryption key must not be empty")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("secrets: creating store directory: %w", err)
	}
	s := &SecretStore{path: path}
	s.key = sha256.Sum256(key)
	return s, nil
}

// ValidateName reports whether name is a legal secret name (alphanumeric and
// underscore only).
func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("secrets: invalid name %q (only letters, digits, and underscore allowed)", name)
	}
	return nil
}

// Set encrypts value and writes it under name.
func (s *SecretStore) Set(name, value string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	enc, err := s.encrypt([]byte(value))
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.fileFor(name), enc, 0o600); err != nil {
		return fmt.Errorf("secrets: writing secret %q: %w", name, err)
	}
	return nil
}

// Get decrypts and returns the value stored under name. It returns
// os.ErrNotExist if no such secret is set.
func (s *SecretStore) Get(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	enc, err := os.ReadFile(s.fileFor(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrNotExist
		}
		return "", fmt.Errorf("secrets: reading secret %q: %w", name, err)
	}
	plain, err := s.decrypt(enc)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// List returns the names of all stored secrets.
func (s *SecretStore) List() ([]string, error) {
	entries, err := os.ReadDir(s.path)
	if err != nil {
		return nil, fmt.Errorf("secrets: reading store directory: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), secretExt) {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), secretExt))
	}
	return names, nil
}

// Delete removes the secret named name. It is idempotent: a missing secret is
// not an error.
func (s *SecretStore) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	err := os.Remove(s.fileFor(name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("secrets: deleting secret %q: %w", name, err)
	}
	return nil
}

// Exists reports whether a secret named name is set, without decrypting it.
func (s *SecretStore) Exists(name string) (bool, error) {
	if err := ValidateName(name); err != nil {
		return false, err
	}
	_, err := os.Stat(s.fileFor(name))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("secrets: checking secret %q: %w", name, err)
}

// Adapter returns this store wrapped as a local Adapter, so callers can treat
// the on-disk store uniformly with the external secret backends.
func (s *SecretStore) Adapter() Adapter {
	return NewLocalAdapter(s)
}

// fileFor returns the on-disk path for a secret.
func (s *SecretStore) fileFor(name string) string {
	return filepath.Join(s.path, name+secretExt)
}

// encrypt seals plaintext with XChaCha20-Poly1305, prefixing the random nonce.
func (s *SecretStore) encrypt(plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(s.key[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

// decrypt reverses encrypt.
func (s *SecretStore) decrypt(data []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(s.key[:])
	if err != nil {
		return nil, err
	}
	if len(data) < aead.NonceSize() {
		return nil, errors.New("secrets: ciphertext too short")
	}
	nonce, ct := data[:aead.NonceSize()], data[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: decrypting (wrong key or corrupt file): %w", err)
	}
	return plain, nil
}
