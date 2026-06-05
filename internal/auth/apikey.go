package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is the work factor used when hashing API-key secrets.
const bcryptCost = 12

// API-key store errors.
var (
	// ErrNotFound is returned when no stored key matches a presented secret.
	ErrNotFound = errors.New("auth: api key not found")
	// ErrExpired is returned when a matching key has passed its ExpiresAt.
	ErrExpired = errors.New("auth: api key expired")
)

// APIKey is an issued credential. The plaintext secret is never stored; only its
// bcrypt hash is kept, so a leaked store cannot be used to authenticate.
type APIKey struct {
	ID          string    `json:"id"`   // public identifier (16 hex chars)
	Hash        string    `json:"hash"` // bcrypt hash of the secret
	UserID      string    `json:"user_id"`
	OrgID       string    `json:"org_id"`
	Roles       []Role    `json:"roles"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"` // zero = never expires
	Description string    `json:"description"`
}

// APIKeyStore holds issued API keys in memory, with optional JSON persistence.
// It is safe for concurrent use.
type APIKeyStore struct {
	mu   sync.RWMutex
	keys map[string]APIKey // keyed by APIKey.ID
}

// NewAPIKeyStore returns an empty store.
func NewAPIKeyStore() *APIKeyStore {
	return &APIKeyStore{keys: make(map[string]APIKey)}
}

// Issue creates a new API key for userID/orgID with the given roles. It returns
// the stored APIKey (hash only) and the plaintext secret, which is shown to the
// caller exactly once and never persisted. ttl=0 means the key never expires.
func (s *APIKeyStore) Issue(userID, orgID string, roles []Role, desc string, ttl time.Duration) (APIKey, string, error) {
	id, err := randomHex(8) // 16 hex chars
	if err != nil {
		return APIKey{}, "", err
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return APIKey{}, "", fmt.Errorf("auth: generating secret: %w", err)
	}
	// The presented secret is "<id>.<hex>" so Verify can look up the key by ID
	// directly instead of bcrypt-comparing against every stored key.
	secret := id + "." + hex.EncodeToString(secretBytes)

	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcryptCost)
	if err != nil {
		return APIKey{}, "", fmt.Errorf("auth: hashing secret: %w", err)
	}

	key := APIKey{
		ID:          id,
		Hash:        string(hash),
		UserID:      userID,
		OrgID:       orgID,
		Roles:       roles,
		CreatedAt:   time.Now().UTC(),
		Description: desc,
	}
	if ttl > 0 {
		key.ExpiresAt = key.CreatedAt.Add(ttl)
	}

	s.mu.Lock()
	s.keys[id] = key
	s.mu.Unlock()
	return key, secret, nil
}

// Verify checks a presented secret and returns the matching key. The secret
// carries its key ID as a prefix ("<id>.<random>"), so verification is a single
// bcrypt comparison against that key. It returns ErrNotFound when no key matches
// and ErrExpired when the matching key has expired.
func (s *APIKeyStore) Verify(secret string) (APIKey, error) {
	id, ok := keyIDFromSecret(secret)
	if !ok {
		return APIKey{}, ErrNotFound
	}

	s.mu.RLock()
	key, present := s.keys[id]
	s.mu.RUnlock()
	if !present {
		return APIKey{}, ErrNotFound
	}

	if bcrypt.CompareHashAndPassword([]byte(key.Hash), []byte(secret)) != nil {
		return APIKey{}, ErrNotFound
	}
	if !key.ExpiresAt.IsZero() && time.Now().After(key.ExpiresAt) {
		return APIKey{}, ErrExpired
	}
	return key, nil
}

// Revoke removes the key with the given ID. It is idempotent.
func (s *APIKeyStore) Revoke(id string) error {
	s.mu.Lock()
	delete(s.keys, id)
	s.mu.Unlock()
	return nil
}

// List returns all keys for orgID with their hashes redacted (the public view).
func (s *APIKeyStore) List(orgID string) []APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []APIKey
	for _, k := range s.keys {
		if k.OrgID != orgID {
			continue
		}
		k.Hash = "" // never expose the hash through List
		out = append(out, k)
	}
	return out
}

// Save persists the store to path as JSON (hashes only, never secrets).
func (s *APIKeyStore) Save(path string) error {
	s.mu.RLock()
	keys := make([]APIKey, 0, len(s.keys))
	for _, k := range s.keys {
		keys = append(keys, k)
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return fmt.Errorf("auth: encoding key store: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("auth: writing key store %s: %w", path, err)
	}
	return nil
}

// Load replaces the store's contents with the keys in the JSON file at path. A
// missing file is treated as an empty store (not an error).
func (s *APIKeyStore) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("auth: reading key store %s: %w", path, err)
	}
	var keys []APIKey
	if err := json.Unmarshal(data, &keys); err != nil {
		return fmt.Errorf("auth: decoding key store: %w", err)
	}
	m := make(map[string]APIKey, len(keys))
	for _, k := range keys {
		m[k.ID] = k
	}
	s.mu.Lock()
	s.keys = m
	s.mu.Unlock()
	return nil
}

// randomHex returns n random bytes hex-encoded (2n characters).
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generating random id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// keyIDFromSecret extracts the key ID prefix from a presented secret of the form
// "<id>.<random>".
func keyIDFromSecret(secret string) (string, bool) {
	for i := 0; i < len(secret); i++ {
		if secret[i] == '.' {
			if i == 0 {
				return "", false
			}
			return secret[:i], true
		}
	}
	return "", false
}
