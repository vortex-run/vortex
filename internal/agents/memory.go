package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryMessage is one persisted turn in a conversation.
type MemoryMessage struct {
	Role      string    `json:"role"` // "user" | "agent" | "system"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	ToolCalls []string  `json:"tool_calls,omitempty"`
}

// memoryData is the serialisable payload of a Memory (no mutex, so json never
// reflects over a lock that another goroutine may hold — which would race).
type memoryData struct {
	SessionID string          `json:"session_id"`
	Messages  []MemoryMessage `json:"messages"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Memory is a per-session conversation history persisted to disk under
// storePath/<sessionID>.json.
type Memory struct {
	SessionID string          `json:"session_id"`
	Messages  []MemoryMessage `json:"messages"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`

	mu        sync.Mutex
	storePath string
}

// snapshot returns a lock-free copy of the serialisable fields (caller holds
// mu). Marshaling this copy avoids json reflecting over the live mutex.
func (m *Memory) snapshot() memoryData {
	msgs := make([]MemoryMessage, len(m.Messages))
	copy(msgs, m.Messages)
	return memoryData{
		SessionID: m.SessionID, Messages: msgs,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// NewMemory creates an in-memory conversation bound to a store directory. Call
// Load to populate it from disk, or Append + Save to persist.
func NewMemory(storePath string) *Memory {
	return &Memory{storePath: storePath, CreatedAt: time.Now(), UpdatedAt: time.Now()}
}

// Append adds a message and bumps UpdatedAt.
func (m *Memory) Append(role, content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	m.Messages = append(m.Messages, MemoryMessage{Role: role, Content: content, Timestamp: time.Now()})
	m.UpdatedAt = time.Now()
}

// path returns the on-disk file for this memory's session. filepath.Base
// strips any directory components from the session ID as defense-in-depth
// against path traversal (production audit H1) — the API layer already
// validates the ID, but the store must not trust it.
func (m *Memory) path() string {
	return filepath.Join(m.storePath, filepath.Base(m.SessionID)+".json")
}

// Save writes the conversation to storePath/<sessionID>.json.
func (m *Memory) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.SessionID == "" {
		return nil
	}
	if err := os.MkdirAll(m.storePath, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.snapshot(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path(), data, 0o600)
}

// Load reads a session's conversation from disk into this Memory.
func (m *Memory) Load(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SessionID = sessionID
	data, err := os.ReadFile(m.path())
	if err != nil {
		return err
	}
	var d memoryData
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	m.SessionID = d.SessionID
	m.Messages = d.Messages
	m.CreatedAt = d.CreatedAt
	m.UpdatedAt = d.UpdatedAt
	return nil
}

// Recent returns the last n messages (for AI context). n<=0 returns all.
func (m *Memory) Recent(n int) []MemoryMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n <= 0 || n >= len(m.Messages) {
		out := make([]MemoryMessage, len(m.Messages))
		copy(out, m.Messages)
		return out
	}
	out := make([]MemoryMessage, n)
	copy(out, m.Messages[len(m.Messages)-n:])
	return out
}

// Summary returns the first user message (a conversation title), or "".
func (m *Memory) Summary() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return summaryOf(m.Messages)
}

// summaryOf returns the first user message (a conversation title), or "".
func summaryOf(msgs []MemoryMessage) string {
	for _, msg := range msgs {
		if msg.Role == "user" {
			return truncateMemoryTitle(msg.Content)
		}
	}
	return ""
}

// SessionInfo describes a stored session for listings.
type SessionInfo struct {
	SessionID string    `json:"session_id"`
	Summary   string    `json:"summary"`
	UpdatedAt time.Time `json:"updated_at"`
}

// List returns all stored sessions (newest first), reading summaries from disk.
func (m *Memory) List() []SessionInfo {
	entries, err := os.ReadDir(m.storePath)
	if err != nil {
		return nil
	}
	var out []SessionInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(m.storePath, e.Name()))
		if rerr != nil {
			continue
		}
		var d memoryData
		if json.Unmarshal(data, &d) != nil {
			continue
		}
		out = append(out, SessionInfo{
			SessionID: d.SessionID,
			Summary:   summaryOf(d.Messages),
			UpdatedAt: d.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// truncateMemoryTitle clamps a title to a readable length.
func truncateMemoryTitle(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 60 {
		return s[:57] + "…"
	}
	return s
}
