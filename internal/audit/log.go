// Package audit implements VORTEX's tamper-proof audit log (build plan M3.6): an
// append-only, HMAC-SHA256 hash-chained record of security-relevant events. Each
// entry's hash covers the previous entry's hash, so removing, reordering, or
// modifying any entry breaks the chain and is detected by Verify. It uses only
// the standard library (crypto/hmac, crypto/sha256).
package audit

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Entry is a single audit record. Hash is the HMAC chaining this entry to the
// previous one; it is computed over the previous hash plus this entry's fields.
type Entry struct {
	Seq       uint64         `json:"seq"`
	Timestamp time.Time      `json:"timestamp"`
	Actor     string         `json:"actor"`    // user ID, "system", "cli", or an IP
	Action    string         `json:"action"`   // e.g. "config.reload", "secret.set"
	Resource  string         `json:"resource"` // what was acted on
	Detail    map[string]any `json:"detail,omitempty"`
	Hash      string         `json:"hash"`
}

// Log is an append-only audit log backed by a newline-delimited JSON file. It is
// safe for concurrent Append calls.
type Log struct {
	path     string
	key      []byte
	mu       sync.Mutex
	lastSeq  uint64
	lastHash string
}

// NewLog opens (creating if needed) the audit log at path and reads the tail so
// new entries continue the existing chain. hmacKey keys the HMAC-SHA256 chain.
func NewLog(path string, hmacKey []byte) (*Log, error) {
	if len(hmacKey) == 0 {
		return nil, fmt.Errorf("audit: hmac key must not be empty")
	}
	l := &Log{path: path, key: hmacKey}

	// Ensure the file exists so the first Append can open it for appending.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: opening log %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	// Scan to the last entry to recover the chain tail.
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("audit: corrupt log line: %w", err)
		}
		l.lastSeq = e.Seq
		l.lastHash = e.Hash
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("audit: reading log: %w", err)
	}
	return l, nil
}

// computeHash returns the hex HMAC-SHA256 chaining prevHash with the entry's
// canonical fields. The canonical form is a fixed-order concatenation so the
// computation is deterministic and order-sensitive.
func (l *Log) computeHash(prevHash string, seq uint64, ts time.Time, actor, action, resource string) string {
	mac := hmac.New(sha256.New, l.key)
	// Field separators (\x1f, the ASCII unit separator) prevent ambiguity
	// between adjacent fields (e.g. actor "a"+action "bc" vs "ab"+"c").
	const sep = "\x1f"
	mac.Write([]byte(prevHash + sep +
		strconv.FormatUint(seq, 10) + sep +
		strconv.FormatInt(ts.UTC().UnixNano(), 10) + sep +
		actor + sep + action + sep + resource))
	return hex.EncodeToString(mac.Sum(nil))
}

// Append writes a new entry to the log with the next sequence number and a hash
// chaining it to the previous entry. It is safe for concurrent use.
func (l *Log) Append(_ context.Context, actor, action, resource string, detail map[string]any) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	seq := l.lastSeq + 1
	ts := time.Now().UTC()
	hash := l.computeHash(l.lastHash, seq, ts, actor, action, resource)

	entry := Entry{
		Seq: seq, Timestamp: ts, Actor: actor, Action: action,
		Resource: resource, Detail: detail, Hash: hash,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("audit: encoding entry: %w", err)
	}

	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("audit: opening log for append: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("audit: writing entry: %w", err)
	}

	l.lastSeq = seq
	l.lastHash = hash
	return nil
}

// Verify reads every entry and recomputes the hash chain. It returns an error
// naming the first entry whose recomputed hash does not match (tamper, deletion,
// or reordering), or nil if the chain is intact.
func (l *Log) Verify() error {
	entries, err := l.readAll()
	if err != nil {
		return err
	}
	prevHash := ""
	var expectSeq uint64 = 1
	for _, e := range entries {
		if e.Seq != expectSeq {
			return fmt.Errorf("audit: chain broken at entry %d: expected seq %d, got %d (entry deleted or reordered)", expectSeq, expectSeq, e.Seq)
		}
		want := l.computeHash(prevHash, e.Seq, e.Timestamp, e.Actor, e.Action, e.Resource)
		if !hmac.Equal([]byte(want), []byte(e.Hash)) {
			return fmt.Errorf("audit: chain broken at entry %d: hash mismatch (entry modified)", e.Seq)
		}
		prevHash = e.Hash
		expectSeq++
	}
	return nil
}

// QueryFilter restricts which entries Query returns. Zero-valued fields are not
// applied. Since and Until bound the timestamp (inclusive). Limit caps the
// result count and, when set, returns the newest matching entries first.
type QueryFilter struct {
	Actor    string
	Action   string
	Resource string
	Since    time.Time
	Until    time.Time
	Limit    int
}

// Query returns the entries matching filter. Without a Limit results are in
// chronological (oldest-first) order; with a Limit the newest matches are
// returned first, capped at Limit.
func (l *Log) Query(filter QueryFilter) ([]Entry, error) {
	entries, err := l.readAll()
	if err != nil {
		return nil, err
	}
	var matched []Entry
	for _, e := range entries {
		if !filter.matches(e) {
			continue
		}
		matched = append(matched, e)
	}

	if filter.Limit > 0 {
		// Newest-first, then cap.
		sort.Slice(matched, func(i, j int) bool { return matched[i].Seq > matched[j].Seq })
		if len(matched) > filter.Limit {
			matched = matched[:filter.Limit]
		}
	}
	return matched, nil
}

// matches reports whether e satisfies the filter.
func (f QueryFilter) matches(e Entry) bool {
	if f.Actor != "" && e.Actor != f.Actor {
		return false
	}
	if f.Action != "" && e.Action != f.Action {
		return false
	}
	if f.Resource != "" && e.Resource != f.Resource {
		return false
	}
	if !f.Since.IsZero() && e.Timestamp.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && e.Timestamp.After(f.Until) {
		return false
	}
	return true
}

// readAll reads and decodes every entry in chronological order.
func (l *Log) readAll() ([]Entry, error) {
	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("audit: opening log: %w", err)
	}
	defer func() { _ = f.Close() }()

	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("audit: corrupt log line: %w", err)
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("audit: reading log: %w", err)
	}
	return entries, nil
}
