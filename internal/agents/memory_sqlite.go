package agents

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// randomSessionID returns a 32-hex-char session identifier (16 random bytes).
func randomSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("agents: generating session id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// MemoryStore persists agent conversations in a single SQLite database (build
// plan M20), replacing the one-JSON-file-per-session layout. SQLite gives
// indexed queries, full-text search across all messages, and O(1) session
// listing instead of the O(n) directory scan the JSON store needed (production
// audit L2). It is safe for concurrent use — the database/sql pool serialises
// writes and SQLite is opened in WAL mode for concurrent reads.
type MemoryStore struct {
	db   *sql.DB
	path string
}

// schema is applied on open; CREATE ... IF NOT EXISTS makes it idempotent. An
// FTS5 virtual table mirrors message content for SearchMessages; triggers keep
// it in sync with the messages table.
const schema = `
CREATE TABLE IF NOT EXISTS sessions (
  id         TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  summary    TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS messages (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  role       TEXT NOT NULL,
  content    TEXT NOT NULL,
  timestamp  INTEGER NOT NULL,
  tool_calls TEXT NOT NULL DEFAULT '[]',
  FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, timestamp);

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
  content, session_id UNINDEXED, content_rowid UNINDEXED
);
CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, content, session_id, content_rowid)
  VALUES (new.id, new.content, new.session_id, new.id);
END;
CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
  DELETE FROM messages_fts WHERE rowid = old.id;
END;
`

// NewMemoryStore opens (creating if needed) a SQLite-backed conversation store
// at dbPath, applying the schema and enabling WAL + foreign keys.
func NewMemoryStore(dbPath string) (*MemoryStore, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("agents: creating memory dir: %w", err)
		}
	}
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("agents: opening memory db: %w", err)
	}
	// SQLite is a single file; one writer at a time avoids "database is locked".
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agents: applying memory schema: %w", err)
	}
	return &MemoryStore{db: db, path: dbPath}, nil
}

// Close closes the underlying database.
func (m *MemoryStore) Close() error { return m.db.Close() }

// NewSession creates a session row with a generated ID and returns it.
func (m *MemoryStore) NewSession() (string, error) {
	id, err := randomSessionID()
	if err != nil {
		return "", err
	}
	now := time.Now().UnixMilli()
	if _, err := m.db.Exec(
		`INSERT INTO sessions(id, created_at, updated_at, summary) VALUES(?,?,?,'')`,
		id, now, now); err != nil {
		return "", fmt.Errorf("agents: creating session: %w", err)
	}
	return id, nil
}

// AppendMessage adds a message to a session, creating the session if needed and
// updating its summary (first user message) and updated_at.
func (m *MemoryStore) AppendMessage(sessionID, role, content string, tools []string) error {
	if sessionID == "" {
		return fmt.Errorf("agents: session id required")
	}
	now := time.Now().UnixMilli()
	toolsJSON, _ := json.Marshal(tools)
	if tools == nil {
		toolsJSON = []byte("[]")
	}

	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Upsert the session.
	if _, err := tx.Exec(
		`INSERT INTO sessions(id, created_at, updated_at, summary) VALUES(?,?,?,'')
		 ON CONFLICT(id) DO UPDATE SET updated_at=excluded.updated_at`,
		sessionID, now, now); err != nil {
		return fmt.Errorf("agents: upserting session: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO messages(session_id, role, content, timestamp, tool_calls) VALUES(?,?,?,?,?)`,
		sessionID, role, content, now, string(toolsJSON)); err != nil {
		return fmt.Errorf("agents: inserting message: %w", err)
	}
	// Set the summary from the first user message if not already set.
	if role == "user" {
		if _, err := tx.Exec(
			`UPDATE sessions SET summary=? WHERE id=? AND summary=''`,
			truncateMemoryTitle(content), sessionID); err != nil {
			return fmt.Errorf("agents: updating summary: %w", err)
		}
	}
	return tx.Commit()
}

// Recent returns the last n messages for a session in chronological order.
// n<=0 returns all.
func (m *MemoryStore) Recent(sessionID string, n int) ([]MemoryMessage, error) {
	// Fetch newest n, then reverse to chronological order.
	limit := "-1"
	if n > 0 {
		limit = fmt.Sprintf("%d", n)
	}
	rows, err := m.db.Query(
		`SELECT role, content, timestamp, tool_calls FROM messages
		 WHERE session_id=? ORDER BY timestamp DESC, id DESC LIMIT `+limit, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var msgs []MemoryMessage
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to chronological.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// ListSessions returns all sessions, newest-updated first.
func (m *MemoryStore) ListSessions() ([]SessionInfo, error) {
	rows, err := m.db.Query(
		`SELECT id, summary, updated_at FROM sessions ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []SessionInfo
	for rows.Next() {
		var id, summary string
		var updated int64
		if err := rows.Scan(&id, &summary, &updated); err != nil {
			return nil, err
		}
		out = append(out, SessionInfo{
			SessionID: id, Summary: summary, UpdatedAt: time.UnixMilli(updated),
		})
	}
	return out, rows.Err()
}

// SearchMessages runs a full-text search across all message content and returns
// matching messages (with their session) newest-first, capped at 100.
func (m *MemoryStore) SearchMessages(query string) ([]SearchResult, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	rows, err := m.db.Query(
		`SELECT m.session_id, m.role, m.content, m.timestamp, m.tool_calls
		 FROM messages_fts f JOIN messages m ON m.id = f.rowid
		 WHERE messages_fts MATCH ? ORDER BY m.timestamp DESC LIMIT 100`,
		ftsQuote(q))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []SearchResult
	for rows.Next() {
		var sessionID string
		var role, content, tools string
		var ts int64
		if err := rows.Scan(&sessionID, &role, &content, &ts, &tools); err != nil {
			return nil, err
		}
		var toolCalls []string
		_ = json.Unmarshal([]byte(tools), &toolCalls)
		out = append(out, SearchResult{
			SessionID: sessionID,
			Message: MemoryMessage{
				Role: role, Content: content,
				Timestamp: time.UnixMilli(ts), ToolCalls: toolCalls,
			},
		})
	}
	return out, rows.Err()
}

// DeleteSession removes a session and its messages (cascade).
func (m *MemoryStore) DeleteSession(id string) error {
	_, err := m.db.Exec(`DELETE FROM sessions WHERE id=?`, id)
	return err
}

// Stats returns aggregate counts and the database file size.
func (m *MemoryStore) Stats() MemoryStats {
	var s MemoryStats
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&s.TotalSessions)
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&s.TotalMessages)
	if info, err := os.Stat(m.path); err == nil {
		s.DBSizeMB = float64(info.Size()) / (1024 * 1024)
	}
	return s
}

// SearchResult is one full-text search hit.
type SearchResult struct {
	SessionID string        `json:"session_id"`
	Message   MemoryMessage `json:"message"`
}

// MemoryStats summarises the store for the dashboard/CLI.
type MemoryStats struct {
	TotalSessions int     `json:"total_sessions"`
	TotalMessages int     `json:"total_messages"`
	DBSizeMB      float64 `json:"db_size_mb"`
}

// MigrateJSONDir imports legacy <sessionID>.json conversation files from dir
// into the store, skipping sessions that already exist. It returns the number
// of sessions imported. A missing/empty dir imports nothing (not an error), so
// it is safe to call on every startup.
func (m *MemoryStore) MigrateJSONDir(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	imported := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			continue
		}
		var d memoryData
		if json.Unmarshal(data, &d) != nil || d.SessionID == "" {
			continue
		}
		// Skip sessions already present (idempotent migration).
		var exists int
		_ = m.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, d.SessionID).Scan(&exists)
		if exists > 0 {
			continue
		}
		if err := m.importSession(d); err != nil {
			return imported, err
		}
		imported++
	}
	return imported, nil
}

// importSession inserts a legacy conversation, preserving timestamps.
func (m *MemoryStore) importSession(d memoryData) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	created := d.CreatedAt.UnixMilli()
	updated := d.UpdatedAt.UnixMilli()
	if _, err := tx.Exec(
		`INSERT INTO sessions(id, created_at, updated_at, summary) VALUES(?,?,?,?)`,
		d.SessionID, created, updated, summaryOf(d.Messages)); err != nil {
		return err
	}
	for _, msg := range d.Messages {
		toolsJSON, _ := json.Marshal(msg.ToolCalls)
		if msg.ToolCalls == nil {
			toolsJSON = []byte("[]")
		}
		if _, err := tx.Exec(
			`INSERT INTO messages(session_id, role, content, timestamp, tool_calls) VALUES(?,?,?,?,?)`,
			d.SessionID, msg.Role, msg.Content, msg.Timestamp.UnixMilli(), string(toolsJSON)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// scanMessage reads one message row.
func scanMessage(rows *sql.Rows) (MemoryMessage, error) {
	var role, content, tools string
	var ts int64
	if err := rows.Scan(&role, &content, &ts, &tools); err != nil {
		return MemoryMessage{}, err
	}
	var toolCalls []string
	_ = json.Unmarshal([]byte(tools), &toolCalls)
	return MemoryMessage{
		Role: role, Content: content,
		Timestamp: time.UnixMilli(ts), ToolCalls: toolCalls,
	}, nil
}

// ftsQuote wraps a query so FTS5 treats it as a phrase of literal terms,
// preventing user input from being parsed as FTS operators/syntax.
func ftsQuote(q string) string {
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}
