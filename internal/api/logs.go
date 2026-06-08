package api

import (
	"net/http"
	"strconv"
	"sync"
)

// LogEntry is one structured log line captured for the TUI/log viewer.
type LogEntry struct {
	Time   string            `json:"time"`
	Level  string            `json:"level"`
	Msg    string            `json:"msg"`
	Fields map[string]string `json:"fields,omitempty"`
}

// LogBuffer is a fixed-capacity ring buffer of recent log entries, safe for
// concurrent writes (the logger) and reads (the /api/logs handler).
type LogBuffer struct {
	mu    sync.RWMutex
	lines []LogEntry
	cap   int
}

// NewLogBuffer creates a buffer holding the last cap entries (min 1).
func NewLogBuffer(capacity int) *LogBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &LogBuffer{cap: capacity}
}

// Write appends an entry, evicting the oldest when at capacity.
func (b *LogBuffer) Write(e LogEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lines) >= b.cap {
		// Drop the oldest (shift) — simple and correct for modest caps.
		copy(b.lines, b.lines[1:])
		b.lines[len(b.lines)-1] = e
		return
	}
	b.lines = append(b.lines, e)
}

// Last returns the most recent n entries (oldest→newest), thread-safe.
func (b *LogBuffer) Last(n int) []LogEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if n <= 0 || n > len(b.lines) {
		n = len(b.lines)
	}
	out := make([]LogEntry, n)
	copy(out, b.lines[len(b.lines)-n:])
	return out
}

// Len returns the number of buffered entries.
func (b *LogBuffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.lines)
}

// SetLogBuffer wires the log ring buffer backing GET /api/logs.
func (s *Server) SetLogBuffer(b *LogBuffer) { s.logBuffer = b }

// handleLogs returns the last N log entries (GET /api/logs?limit=N).
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.logBuffer == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"entries": []LogEntry{}})
		return
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 1000 {
		limit = 1000
	}
	entries := s.logBuffer.Last(limit)
	s.writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
