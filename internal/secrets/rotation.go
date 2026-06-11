package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// metaExt is the file extension for secret metadata, stored beside the
// encrypted value. Metadata holds lifecycle facts only — never the value.
const metaExt = ".meta"

// RotationWarningWindow is how far ahead of an expiry or rotation deadline
// DueForRotation starts reporting true.
const RotationWarningWindow = 7 * 24 * time.Hour

// SecretMetadata tracks a secret's lifecycle for expiry and rotation alerts
// (build plan M19).
type SecretMetadata struct {
	Name        string        `json:"name"`
	CreatedAt   time.Time     `json:"created_at"`
	ExpiresAt   time.Time     `json:"expires_at,omitzero"` // zero = never expires
	LastRotated time.Time     `json:"last_rotated"`
	RotateEvery time.Duration `json:"rotate_every,omitempty"` // 0 = manual only
	Version     int           `json:"version"`
}

// SetWithMetadata stores value under name (encrypted, like Set) and records
// lifecycle metadata beside it. Zero CreatedAt/LastRotated default to now;
// a zero Version auto-increments from the previous metadata (starting at 1).
func (s *SecretStore) SetWithMetadata(name, value string, meta SecretMetadata) error {
	prev, err := s.GetMetadata(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.Set(name, value); err != nil {
		return err
	}

	now := time.Now()
	meta.Name = name
	if meta.CreatedAt.IsZero() {
		if prev != nil {
			meta.CreatedAt = prev.CreatedAt
		} else {
			meta.CreatedAt = now
		}
	}
	if meta.LastRotated.IsZero() {
		meta.LastRotated = now
	}
	if meta.Version == 0 {
		if prev != nil {
			meta.Version = prev.Version + 1
		} else {
			meta.Version = 1
		}
	}

	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("secrets: encoding metadata for %q: %w", name, err)
	}
	if err := os.WriteFile(s.metaFileFor(name), b, 0o600); err != nil {
		return fmt.Errorf("secrets: writing metadata for %q: %w", name, err)
	}
	return nil
}

// GetMetadata returns the lifecycle metadata for name, or os.ErrNotExist when
// the secret has none (set via plain Set, or never set).
func (s *SecretStore) GetMetadata(name string) (*SecretMetadata, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.metaFileFor(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("secrets: reading metadata for %q: %w", name, err)
	}
	var meta SecretMetadata
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, fmt.Errorf("secrets: parsing metadata for %q: %w", name, err)
	}
	return &meta, nil
}

// IsExpired reports whether name's expiry has passed. Secrets without
// metadata or without an ExpiresAt never expire.
func (s *SecretStore) IsExpired(name string) (bool, error) {
	meta, err := s.GetMetadata(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if meta.ExpiresAt.IsZero() {
		return false, nil
	}
	return time.Now().After(meta.ExpiresAt), nil
}

// DueForRotation reports whether name is inside the warning window (7 days)
// of its expiry, or past LastRotated+RotateEvery minus the window when a
// rotation interval is set. Secrets without metadata are never due.
func (s *SecretStore) DueForRotation(name string) (bool, error) {
	meta, err := s.GetMetadata(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	now := time.Now()
	if !meta.ExpiresAt.IsZero() && now.After(meta.ExpiresAt.Add(-RotationWarningWindow)) {
		return true, nil
	}
	if meta.RotateEvery > 0 {
		deadline := meta.LastRotated.Add(meta.RotateEvery)
		if now.After(deadline.Add(-RotationWarningWindow)) {
			return true, nil
		}
	}
	return false, nil
}

// RotationAlert describes one secret needing operator attention: already
// expired, or due for rotation within the warning window.
type RotationAlert struct {
	Name     string
	Expired  bool      // true: past expiry; false: rotation due
	Deadline time.Time // the expiry or rotation deadline that triggered this
}

// CheckRotation scans every stored secret's metadata and returns alerts for
// expired and rotation-due secrets, for the startup check and notifications.
func (s *SecretStore) CheckRotation() ([]RotationAlert, error) {
	names, err := s.List()
	if err != nil {
		return nil, err
	}
	var alerts []RotationAlert
	for _, name := range names {
		meta, err := s.GetMetadata(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}

		expired, err := s.IsExpired(name)
		if err != nil {
			return nil, err
		}
		if expired {
			alerts = append(alerts, RotationAlert{Name: name, Expired: true, Deadline: meta.ExpiresAt})
			continue
		}

		due, err := s.DueForRotation(name)
		if err != nil {
			return nil, err
		}
		if due {
			alerts = append(alerts, RotationAlert{Name: name, Deadline: rotationDeadline(meta)})
		}
	}
	return alerts, nil
}

// rotationDeadline returns the earliest of the expiry and the next scheduled
// rotation; callers only use it for metadata that has at least one of them.
func rotationDeadline(meta *SecretMetadata) time.Time {
	deadline := meta.ExpiresAt
	if meta.RotateEvery > 0 {
		next := meta.LastRotated.Add(meta.RotateEvery)
		if deadline.IsZero() || next.Before(deadline) {
			deadline = next
		}
	}
	return deadline
}

// metaFileFor returns the on-disk path for a secret's metadata.
func (s *SecretStore) metaFileFor(name string) string {
	return s.fileFor(name) + metaExt
}

// Rekey re-encrypts every stored secret from this store's current key to
// newKey, used by the master-key migration (production audit C1) to move
// legacy cluster-name-keyed stores onto the master-derived key. On success
// the store's in-memory key is updated to newKey. Metadata files are
// plaintext JSON and are left untouched. It is best-effort atomic per file:
// each secret is decrypted, re-encrypted, and rewritten; a mid-run failure
// leaves already-converted files on the new key and the rest on the old, but
// the returned error names the failure so the caller can retry.
func (s *SecretStore) Rekey(newKey []byte) error {
	if len(newKey) == 0 {
		return errors.New("secrets: new key must not be empty")
	}
	names, err := s.List()
	if err != nil {
		return err
	}
	next := SecretStore{path: s.path}
	next.key = sha256Key(newKey)

	for _, name := range names {
		plain, err := s.Get(name)
		if err != nil {
			return fmt.Errorf("secrets: rekey reading %q: %w", name, err)
		}
		enc, err := next.encrypt([]byte(plain))
		if err != nil {
			return fmt.Errorf("secrets: rekey encrypting %q: %w", name, err)
		}
		if err := os.WriteFile(s.fileFor(name), enc, 0o600); err != nil {
			return fmt.Errorf("secrets: rekey writing %q: %w", name, err)
		}
	}
	s.key = next.key
	return nil
}

// CanDecrypt reports whether at least one stored secret decrypts with this
// store's key — a cheap probe to detect whether a store is on this key
// (used by migration to decide whether re-keying is needed).
func (s *SecretStore) CanDecrypt() bool {
	names, err := s.List()
	if err != nil || len(names) == 0 {
		return true // empty store: nothing to migrate, treat as decryptable
	}
	_, err = s.Get(names[0])
	return err == nil
}
