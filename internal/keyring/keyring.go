// Package keyring manages VORTEX's root key material. Every at-rest
// encryption key (secret store, TLS cert store, mTLS cluster CA) and the
// audit-log HMAC key is derived from a single random 32-byte master key via
// HKDF-SHA256 with a per-purpose label — never from the (public, /health-
// exposed) cluster name (production audit C1).
//
// The master key comes from, in order:
//  1. the VORTEX_MASTER_KEY environment variable (hex or base64, 32 bytes), or
//  2. a 0600 key file generated with crypto/rand on first run.
//
// Stdlib + golang.org/x/crypto/hkdf only.
package keyring

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// MasterKeySize is the master key length in bytes.
const MasterKeySize = 32

// EnvMasterKey overrides the master-key file with an inline hex/base64 key.
const EnvMasterKey = "VORTEX_MASTER_KEY"

// Keyring holds the master key and derives purpose-scoped subkeys from it.
type Keyring struct {
	master []byte
}

// LoadOrCreate resolves the master key. If VORTEX_MASTER_KEY is set it is
// decoded and used (nothing is written to disk). Otherwise the key at path is
// read, or — when absent — a fresh random key is generated and persisted with
// 0600 permissions (its parent directory is created 0700).
func LoadOrCreate(path string) (*Keyring, error) {
	if env := strings.TrimSpace(os.Getenv(EnvMasterKey)); env != "" {
		key, err := decodeKey(env)
		if err != nil {
			return nil, fmt.Errorf("keyring: %s: %w", EnvMasterKey, err)
		}
		return &Keyring{master: key}, nil
	}

	if b, err := os.ReadFile(path); err == nil {
		key, derr := decodeKey(strings.TrimSpace(string(b)))
		if derr != nil {
			return nil, fmt.Errorf("keyring: parsing master key file %s: %w", path, derr)
		}
		return &Keyring{master: key}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("keyring: reading master key file %s: %w", path, err)
	}

	// First run: generate and persist a new random master key.
	key := make([]byte, MasterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("keyring: generating master key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("keyring: creating key directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("keyring: writing master key file %s: %w", path, err)
	}
	return &Keyring{master: key}, nil
}

// FromMaster builds a Keyring from raw master-key bytes (tests, and callers
// that source the key elsewhere). The key must be exactly MasterKeySize bytes.
func FromMaster(master []byte) (*Keyring, error) {
	if len(master) != MasterKeySize {
		return nil, fmt.Errorf("keyring: master key must be %d bytes, got %d", MasterKeySize, len(master))
	}
	dup := make([]byte, len(master))
	copy(dup, master)
	return &Keyring{master: dup}, nil
}

// Subkey derives a 32-byte key for the given purpose label (e.g. "secrets",
// "tls-store", "mtls-store", "audit") via HKDF-SHA256. The same purpose always
// yields the same key for a given master; distinct purposes are independent.
func (k *Keyring) Subkey(purpose string) []byte {
	out := make([]byte, 32)
	r := hkdf.New(sha256.New, k.master, []byte("vortex/keyring/v1"), []byte(purpose))
	// HKDF over SHA-256 cannot fail for a 32-byte output, but guard anyway.
	if _, err := io.ReadFull(r, out); err != nil {
		// Fall back to a direct HMAC-style derivation; unreachable in practice.
		sum := sha256.Sum256(append([]byte(purpose), k.master...))
		copy(out, sum[:])
	}
	return out
}

// decodeKey parses a master key from hex (64 chars) or base64, requiring
// exactly MasterKeySize bytes.
func decodeKey(s string) ([]byte, error) {
	var key []byte
	if b, err := hex.DecodeString(s); err == nil && len(b) == MasterKeySize {
		key = b
	} else if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == MasterKeySize {
		key = b
	} else if b, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(b) == MasterKeySize {
		key = b
	}
	if key == nil {
		return nil, fmt.Errorf("must be %d bytes encoded as hex or base64", MasterKeySize)
	}
	return key, nil
}
