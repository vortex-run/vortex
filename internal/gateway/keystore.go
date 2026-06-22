// Package gateway implements VORTEX's autonomous API-key rotation: persistent
// key slots with health scoring, intelligent routing across slots, and context
// preservation when switching providers mid-session. A degraded or rate-limited
// key is automatically replaced by the next healthy slot without losing the
// conversation.
package gateway

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// KeySlot is one configured API-key slot. The API key is stored encrypted at
// rest (ChaCha20 under the keystore subkey); List/Get return it still
// encrypted, GetDecrypted decrypts it for use.
type KeySlot struct {
	ID          string    `json:"id"`       // "slot-1" .. "slot-4"
	Provider    string    `json:"provider"` // deepseek|claude|openai|gemini|groq|ollama
	APIKey      string    `json:"-"`        // encrypted in storage; never serialised
	Model       string    `json:"model"`
	Priority    int       `json:"priority"` // 1=highest .. 4=lowest
	DailyBudget float64   `json:"daily_budget"`
	Enabled     bool      `json:"enabled"`
	Label       string    `json:"label"`
	AddedAt     time.Time `json:"added_at"`
}

// KeyHealth is a slot's live health snapshot, used for scoring and routing.
type KeyHealth struct {
	SlotID           string    `json:"slot_id"`
	Score            int       `json:"score"`
	RequestsToday    int64     `json:"requests_today"`
	ErrorsLast10     int       `json:"errors_last_10"`
	AvgLatencyMs     int64     `json:"avg_latency_ms"`
	SpentTodayUSD    float64   `json:"spent_today_usd"`
	RateLimited      bool      `json:"rate_limited"`
	RateLimitedUntil time.Time `json:"rate_limited_until"`
	LastUsed         time.Time `json:"last_used"`
	LastError        string    `json:"last_error"`
	LastErrorAt      time.Time `json:"last_error_at"`
}

// CalcScore computes a 0-100 health score from a slot's metrics (rate-limit,
// error streak, latency). The budget penalty lives in ScoreFor, since the
// daily budget is a property of the slot, not the health row. Higher is
// healthier; the router prefers high-scoring slots and skips low ones.
func CalcScore(h KeyHealth) int {
	score := 100
	if h.RateLimited {
		score -= 50
	}
	switch {
	case h.ErrorsLast10 > 5:
		score -= 30
	case h.ErrorsLast10 > 2:
		score -= 15
	}
	switch {
	case h.AvgLatencyMs > 30000:
		score -= 20
	case h.AvgLatencyMs > 10000:
		score -= 10
	}
	if score < 0 {
		return 0
	}
	return score
}

// ScoreFor computes the full score for a health snapshot against a slot's
// daily budget: CalcScore plus a -40 penalty once today's spend reaches the
// budget (a budget of 0 means unlimited, no penalty).
func ScoreFor(h KeyHealth, dailyBudget float64) int {
	base := CalcScore(h)
	if dailyBudget > 0 && h.SpentTodayUSD >= dailyBudget {
		base -= 40
	}
	if base < 0 {
		return 0
	}
	return base
}

// KeyStore persists key slots and their health in SQLite, encrypting API keys
// at rest. It is safe for concurrent use (single connection, WAL mode).
type KeyStore struct {
	db     *sql.DB
	path   string
	encKey []byte
	mu     sync.Mutex
}

// keystoreSchema is applied on open; IF NOT EXISTS makes it idempotent.
const keystoreSchema = `
CREATE TABLE IF NOT EXISTS key_slots (
  id           TEXT PRIMARY KEY,
  provider     TEXT NOT NULL,
  api_key_enc  TEXT NOT NULL,
  model        TEXT NOT NULL DEFAULT '',
  priority     INTEGER NOT NULL DEFAULT 4,
  daily_budget REAL NOT NULL DEFAULT 0,
  enabled      INTEGER NOT NULL DEFAULT 1,
  label        TEXT NOT NULL DEFAULT '',
  added_at     INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS key_health (
  slot_id            TEXT PRIMARY KEY,
  score              INTEGER NOT NULL DEFAULT 100,
  requests_today     INTEGER NOT NULL DEFAULT 0,
  errors_last_10     INTEGER NOT NULL DEFAULT 0,
  avg_latency_ms     INTEGER NOT NULL DEFAULT 0,
  spent_today_usd    REAL NOT NULL DEFAULT 0,
  rate_limited       INTEGER NOT NULL DEFAULT 0,
  rate_limited_until INTEGER NOT NULL DEFAULT 0,
  last_used          INTEGER NOT NULL DEFAULT 0,
  last_error         TEXT NOT NULL DEFAULT '',
  last_error_at      INTEGER NOT NULL DEFAULT 0
);
`

// NewKeyStore opens (creating if needed) a SQLite-backed key store at dbPath,
// applying the schema and enabling WAL. encKey is a 32-byte key (e.g. the
// keyring "keystore" subkey) used to encrypt API keys at rest.
func NewKeyStore(dbPath string, encKey []byte) (*KeyStore, error) {
	if len(encKey) != 32 {
		return nil, fmt.Errorf("gateway: keystore encryption key must be 32 bytes, got %d", len(encKey))
	}
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("gateway: creating keystore dir: %w", err)
		}
	}
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("gateway: opening keystore db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(keystoreSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("gateway: applying keystore schema: %w", err)
	}
	return &KeyStore{db: db, path: dbPath, encKey: encKey}, nil
}

// Close closes the underlying database.
func (s *KeyStore) Close() error { return s.db.Close() }

// encrypt encrypts plaintext with ChaCha20 under the keystore key, returning
// base64(nonce || ciphertext). A random nonce is used per call.
func (s *KeyStore) encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, chacha20.NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	cipher, err := chacha20.NewUnauthenticatedCipher(s.encKey, nonce)
	if err != nil {
		return "", err
	}
	out := make([]byte, len(plaintext))
	cipher.XORKeyStream(out, []byte(plaintext))
	return base64.StdEncoding.EncodeToString(append(nonce, out...)), nil
}

// decrypt reverses encrypt.
func (s *KeyStore) decrypt(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	if len(raw) < chacha20.NonceSize {
		return "", fmt.Errorf("gateway: ciphertext too short")
	}
	nonce, ct := raw[:chacha20.NonceSize], raw[chacha20.NonceSize:]
	cipher, err := chacha20.NewUnauthenticatedCipher(s.encKey, nonce)
	if err != nil {
		return "", err
	}
	out := make([]byte, len(ct))
	cipher.XORKeyStream(out, ct)
	return string(out), nil
}

// Add inserts or replaces a key slot, encrypting its API key, and seeds a
// fresh health row. AddedAt and Enabled default sensibly when zero.
func (s *KeyStore) Add(slot KeySlot) error {
	if slot.ID == "" {
		return fmt.Errorf("gateway: slot id required")
	}
	if slot.Provider == "" {
		return fmt.Errorf("gateway: slot provider required")
	}
	enc, err := s.encrypt(slot.APIKey)
	if err != nil {
		return fmt.Errorf("gateway: encrypting key: %w", err)
	}
	if slot.AddedAt.IsZero() {
		slot.AddedAt = time.Now()
	}
	if slot.Priority <= 0 {
		slot.Priority = 4
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`INSERT INTO key_slots(id, provider, api_key_enc, model, priority, daily_budget, enabled, label, added_at)
		 VALUES(?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   provider=excluded.provider, api_key_enc=excluded.api_key_enc, model=excluded.model,
		   priority=excluded.priority, daily_budget=excluded.daily_budget,
		   enabled=excluded.enabled, label=excluded.label`,
		slot.ID, slot.Provider, enc, slot.Model, slot.Priority, slot.DailyBudget,
		boolToInt(slot.Enabled), slot.Label, slot.AddedAt.UnixMilli()); err != nil {
		return fmt.Errorf("gateway: saving slot: %w", err)
	}
	// Seed a health row if absent (full score until proven otherwise).
	if _, err := tx.Exec(
		`INSERT INTO key_health(slot_id, score) VALUES(?, 100)
		 ON CONFLICT(slot_id) DO NOTHING`, slot.ID); err != nil {
		return fmt.Errorf("gateway: seeding health: %w", err)
	}
	return tx.Commit()
}

// Remove deletes a slot and its health row.
func (s *KeyStore) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`DELETE FROM key_slots WHERE id=?`, id)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM key_health WHERE slot_id=?`, id); err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("gateway: slot %s not found", id)
	}
	return tx.Commit()
}

// List returns all slots ordered by priority (highest first). API keys remain
// encrypted (the APIKey field carries the ciphertext blob).
func (s *KeyStore) List() ([]KeySlot, error) {
	rows, err := s.db.Query(
		`SELECT id, provider, api_key_enc, model, priority, daily_budget, enabled, label, added_at
		 FROM key_slots ORDER BY priority ASC, added_at ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []KeySlot
	for rows.Next() {
		slot, err := scanSlot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, slot)
	}
	return out, rows.Err()
}

// Get returns one slot with its API key still encrypted.
func (s *KeyStore) Get(id string) (*KeySlot, error) {
	row := s.db.QueryRow(
		`SELECT id, provider, api_key_enc, model, priority, daily_budget, enabled, label, added_at
		 FROM key_slots WHERE id=?`, id)
	slot, err := scanSlot(row)
	if err != nil {
		return nil, fmt.Errorf("gateway: slot %s not found: %w", id, err)
	}
	return &slot, nil
}

// GetDecrypted returns one slot with its API key decrypted for use.
func (s *KeyStore) GetDecrypted(id string) (*KeySlot, error) {
	slot, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	key, err := s.decrypt(slot.APIKey)
	if err != nil {
		return nil, fmt.Errorf("gateway: decrypting key: %w", err)
	}
	slot.APIKey = key
	return slot, nil
}

// UpdateHealth persists a slot's health snapshot, recomputing its score from
// the slot's daily budget.
func (s *KeyStore) UpdateHealth(h KeyHealth) error {
	budget := 0.0
	if slot, err := s.Get(h.SlotID); err == nil {
		budget = slot.DailyBudget
	}
	h.Score = ScoreFor(h, budget)

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO key_health(slot_id, score, requests_today, errors_last_10, avg_latency_ms,
		   spent_today_usd, rate_limited, rate_limited_until, last_used, last_error, last_error_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(slot_id) DO UPDATE SET
		   score=excluded.score, requests_today=excluded.requests_today,
		   errors_last_10=excluded.errors_last_10, avg_latency_ms=excluded.avg_latency_ms,
		   spent_today_usd=excluded.spent_today_usd, rate_limited=excluded.rate_limited,
		   rate_limited_until=excluded.rate_limited_until, last_used=excluded.last_used,
		   last_error=excluded.last_error, last_error_at=excluded.last_error_at`,
		h.SlotID, h.Score, h.RequestsToday, h.ErrorsLast10, h.AvgLatencyMs,
		h.SpentTodayUSD, boolToInt(h.RateLimited), h.RateLimitedUntil.UnixMilli(),
		h.LastUsed.UnixMilli(), h.LastError, h.LastErrorAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("gateway: updating health: %w", err)
	}
	return nil
}

// GetHealth returns a slot's health snapshot (a zero-value with full score
// when no row exists yet).
func (s *KeyStore) GetHealth(id string) (*KeyHealth, error) {
	row := s.db.QueryRow(
		`SELECT slot_id, score, requests_today, errors_last_10, avg_latency_ms,
		   spent_today_usd, rate_limited, rate_limited_until, last_used, last_error, last_error_at
		 FROM key_health WHERE slot_id=?`, id)
	h, err := scanHealth(row)
	if err != nil {
		return &KeyHealth{SlotID: id, Score: 100}, nil
	}
	return &h, nil
}

// AllHealth returns every slot's health snapshot.
func (s *KeyStore) AllHealth() ([]KeyHealth, error) {
	rows, err := s.db.Query(
		`SELECT slot_id, score, requests_today, errors_last_10, avg_latency_ms,
		   spent_today_usd, rate_limited, rate_limited_until, last_used, last_error, last_error_at
		 FROM key_health`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []KeyHealth
	for rows.Next() {
		h, err := scanHealth(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// BestSlot returns the highest-scoring enabled slot. It errors when no slot is
// enabled.
func (s *KeyStore) BestSlot() (*KeySlot, error) {
	slots, err := s.List()
	if err != nil {
		return nil, err
	}
	var best *KeySlot
	bestScore := -1
	for i := range slots {
		if !slots[i].Enabled {
			continue
		}
		h, _ := s.GetHealth(slots[i].ID)
		if h.Score > bestScore {
			bestScore = h.Score
			best = &slots[i]
		}
	}
	if best == nil {
		return nil, fmt.Errorf("gateway: no enabled key slots")
	}
	return best, nil
}

// ResetDailyStats zeroes per-day counters (requests + spend) across all slots,
// clears the score penalty by recomputing, and clears stale rate limits. Run
// at midnight.
func (s *KeyStore) ResetDailyStats() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`UPDATE key_health SET requests_today=0, spent_today_usd=0, errors_last_10=0,
		   rate_limited=0, score=100`)
	return err
}

// --- scan helpers -----------------------------------------------------------

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanSlot(r rowScanner) (KeySlot, error) {
	var slot KeySlot
	var added int64
	var enabled int
	if err := r.Scan(&slot.ID, &slot.Provider, &slot.APIKey, &slot.Model,
		&slot.Priority, &slot.DailyBudget, &enabled, &slot.Label, &added); err != nil {
		return KeySlot{}, err
	}
	slot.Enabled = enabled != 0
	slot.AddedAt = time.UnixMilli(added)
	return slot, nil
}

func scanHealth(r rowScanner) (KeyHealth, error) {
	var h KeyHealth
	var rl int
	var until, lastUsed, lastErrAt int64
	if err := r.Scan(&h.SlotID, &h.Score, &h.RequestsToday, &h.ErrorsLast10,
		&h.AvgLatencyMs, &h.SpentTodayUSD, &rl, &until, &lastUsed, &h.LastError, &lastErrAt); err != nil {
		return KeyHealth{}, err
	}
	h.RateLimited = rl != 0
	h.RateLimitedUntil = time.UnixMilli(until)
	h.LastUsed = time.UnixMilli(lastUsed)
	h.LastErrorAt = time.UnixMilli(lastErrAt)
	return h, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
