package orchestration

import (
	"sync"
	"time"
)

// MemoryEntry is one stored value with provenance.
type MemoryEntry struct {
	Key       string    `json:"key"`
	Value     any       `json:"value"`
	Author    string    `json:"author"` // task/agent that wrote it
	UpdatedAt time.Time `json:"updated_at"`
}

// SharedMemory is a concurrency-safe key/value store that agents in an
// orchestration share. Writes are versioned (an append-only history is kept per
// key) so the orchestrator can audit who produced what.
type SharedMemory struct {
	mu      sync.RWMutex
	values  map[string]MemoryEntry
	history map[string][]MemoryEntry
}

// NewSharedMemory constructs an empty store.
func NewSharedMemory() *SharedMemory {
	return &SharedMemory{
		values:  map[string]MemoryEntry{},
		history: map[string][]MemoryEntry{},
	}
}

// Set stores value under key, attributing it to author. It overwrites the
// current value and appends to the key's history.
func (m *SharedMemory) Set(key string, value any, author string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := MemoryEntry{Key: key, Value: value, Author: author, UpdatedAt: time.Now()}
	m.values[key] = entry
	m.history[key] = append(m.history[key], entry)
}

// Get returns the current value for key.
func (m *SharedMemory) Get(key string) (any, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.values[key]
	if !ok {
		return nil, false
	}
	return e.Value, true
}

// GetString returns the value for key as a string ("" if absent/non-string).
func (m *SharedMemory) GetString(key string) string {
	v, ok := m.Get(key)
	if !ok {
		return ""
	}
	if s, isStr := v.(string); isStr {
		return s
	}
	return ""
}

// Has reports whether key is present.
func (m *SharedMemory) Has(key string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.values[key]
	return ok
}

// Delete removes a key's current value (history is retained).
func (m *SharedMemory) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, key)
}

// Keys returns the current keys (unsorted).
func (m *SharedMemory) Keys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.values))
	for k := range m.values {
		keys = append(keys, k)
	}
	return keys
}

// Snapshot returns a copy of all current entries.
func (m *SharedMemory) Snapshot() map[string]MemoryEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]MemoryEntry, len(m.values))
	for k, v := range m.values {
		out[k] = v
	}
	return out
}

// History returns the append-only write history for key.
func (m *SharedMemory) History(key string) []MemoryEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := m.history[key]
	out := make([]MemoryEntry, len(h))
	copy(out, h)
	return out
}

// Len returns the number of current keys.
func (m *SharedMemory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.values)
}
