package agents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// This file implements side-effect fencing for resumable executions
// (production audit H3, increment 2). Crash recovery re-runs interrupted
// tasks at-least-once; without fencing, a re-run re-executes every side
// effect the interrupted attempt already performed (commands, file writes).
// The EffectLedger is a durable journal of side-effecting tool calls: under
// an effect scope, a call whose fingerprint is already journaled replays its
// recorded result instead of executing again, so a resumed task re-runs only
// the tail it never reached.

// effectScopeKey carries the effect scope through a context.
type effectScopeKey struct{}

// WithEffectScope returns a context whose tool executions are fenced under
// scope (e.g. "<runID>/<taskID>" for an orchestration task). Executions
// without a scope are never fenced.
func WithEffectScope(ctx context.Context, scope string) context.Context {
	if scope == "" {
		return ctx
	}
	return context.WithValue(ctx, effectScopeKey{}, scope)
}

// EffectScope returns the effect scope carried by ctx, if any.
func EffectScope(ctx context.Context) (string, bool) {
	s, ok := ctx.Value(effectScopeKey{}).(string)
	return s, ok && s != ""
}

// SideEffecting is implemented by tools whose execution changes external
// state (writes files, runs commands). Only these are fenced by the ledger —
// read-only tools always execute.
type SideEffecting interface {
	SideEffecting() bool
}

// EffectLedger journals side-effecting tool calls in SQLite (same conventions
// as MemoryStore / the orchestration RunStore: WAL, serialised writes). It is
// safe for concurrent use.
type EffectLedger struct {
	db   *sql.DB
	path string

	// occ tracks, per scope, how many times each call fingerprint has been
	// issued by THIS process, so two intentional identical calls within one
	// scope get distinct keys ("…#1", "…#2"). A resumed attempt counts from 1
	// again in the same order, which is exactly what replay requires.
	mu  sync.Mutex
	occ map[string]map[string]int
}

const effectSchema = `
CREATE TABLE IF NOT EXISTS effects (
	scope      TEXT NOT NULL,
	key        TEXT NOT NULL,
	result     TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	PRIMARY KEY (scope, key)
);`

// NewEffectLedger opens (or creates) the ledger database at path.
func NewEffectLedger(path string) (*EffectLedger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("agents: effect ledger dir: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("agents: opening effect ledger: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(effectSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agents: effect ledger schema: %w", err)
	}
	return &EffectLedger{db: db, path: path, occ: map[string]map[string]int{}}, nil
}

// Close releases the underlying database.
func (l *EffectLedger) Close() error { return l.db.Close() }

// CallKey fingerprints one tool call under scope: a hash of the tool name and
// its canonical (sorted-key JSON) parameters, suffixed with this process's
// occurrence count for that fingerprint. Deterministic across a replayed
// attempt that issues the same calls in the same order.
func (l *EffectLedger) CallKey(scope, tool string, params map[string]any) string {
	canonical, _ := json.Marshal(params) // Go maps marshal with sorted keys
	sum := sha256.Sum256(append([]byte(tool+"\x00"), canonical...))
	fp := hex.EncodeToString(sum[:16])

	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.occ[scope]
	if m == nil {
		m = map[string]int{}
		l.occ[scope] = m
	}
	m[fp]++
	return fmt.Sprintf("%s#%d", fp, m[fp])
}

// Lookup returns the journaled result for (scope, key), if committed.
func (l *EffectLedger) Lookup(scope, key string) (result string, ok bool) {
	row := l.db.QueryRow(`SELECT result FROM effects WHERE scope = ? AND key = ?`, scope, key)
	if err := row.Scan(&result); err != nil {
		return "", false
	}
	return result, true
}

// Commit journals the result of (scope, key). Re-commits are idempotent.
func (l *EffectLedger) Commit(scope, key, result string) error {
	_, err := l.db.Exec(
		`INSERT OR REPLACE INTO effects (scope, key, result, created_at) VALUES (?, ?, ?, ?)`,
		scope, key, result, time.Now().UTC())
	return err
}

// ClearScope removes every journaled effect under scope (e.g. when a run
// finishes and its journal can never be replayed again).
func (l *EffectLedger) ClearScope(scope string) error {
	_, err := l.db.Exec(`DELETE FROM effects WHERE scope = ?`, scope)
	l.mu.Lock()
	delete(l.occ, scope)
	l.mu.Unlock()
	return err
}

// PruneOlderThan drops journal entries older than d — crash-resume replays
// happen within one process restart, so old entries are dead weight. Called
// once at startup.
func (l *EffectLedger) PruneOlderThan(d time.Duration) error {
	_, err := l.db.Exec(`DELETE FROM effects WHERE created_at < ?`, time.Now().UTC().Add(-d))
	return err
}
