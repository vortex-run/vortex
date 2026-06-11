package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Episode is one important moment remembered across sessions (build plan
// upgrade 2 — tier-2 episodic memory). Where MemoryStore holds the verbatim
// transcript of a single session, episodes are distilled facts ("user prefers
// Python", "deployed to production on 2026-06-09") recalled into prompts for
// any later session.
type Episode struct {
	ID         string    `json:"id"`
	Content    string    `json:"content"`    // what happened
	Context    string    `json:"context"`    // project/task context
	Importance float64   `json:"importance"` // 0.0-1.0
	Tags       []string  `json:"tags"`
	Timestamp  time.Time `json:"timestamp"`
	SessionID  string    `json:"session_id"`
}

// EpisodicStore persists episodes in SQLite with an FTS5 index so Recall can
// match free-form message text. Results are ranked by importance damped by
// age, so a vital old fact can still outrank a trivial recent one. Safe for
// concurrent use (single connection, WAL), mirroring MemoryStore/SkillStore.
type EpisodicStore struct {
	db   *sql.DB
	path string
}

// episodesSchema is applied on open; IF NOT EXISTS makes it idempotent. The
// FTS table is maintained manually inside Store transactions (tags are JSON in
// the main table but indexed as space-joined text).
const episodesSchema = `
CREATE TABLE IF NOT EXISTS episodes (
  id         TEXT PRIMARY KEY,
  content    TEXT NOT NULL,
  context    TEXT NOT NULL DEFAULT '',
  importance REAL NOT NULL DEFAULT 0.5,
  tags_json  TEXT NOT NULL DEFAULT '[]',
  timestamp  INTEGER NOT NULL,
  session_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_episodes_context ON episodes(context);
CREATE VIRTUAL TABLE IF NOT EXISTS episodes_fts USING fts5(
  content, context, tags, episode_id UNINDEXED
);
`

// NewEpisodicStore opens (creating if needed) a SQLite-backed episode store at
// dbPath, applying the schema and enabling WAL + busy timeout.
func NewEpisodicStore(dbPath string) (*EpisodicStore, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("agents: creating episodes dir: %w", err)
		}
	}
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("agents: opening episodes db: %w", err)
	}
	// SQLite is a single file; one writer at a time avoids "database is locked".
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(episodesSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agents: applying episodes schema: %w", err)
	}
	return &EpisodicStore{db: db, path: dbPath}, nil
}

// Close closes the underlying database.
func (s *EpisodicStore) Close() error { return s.db.Close() }

// Store saves an episode, generating an ID and timestamp when absent and
// clamping importance into [0,1].
func (s *EpisodicStore) Store(ep Episode) error {
	if strings.TrimSpace(ep.Content) == "" {
		return fmt.Errorf("agents: episode content required")
	}
	if ep.ID == "" {
		id, err := randomSessionID()
		if err != nil {
			return err
		}
		ep.ID = id
	}
	if ep.Timestamp.IsZero() {
		ep.Timestamp = time.Now()
	}
	if ep.Importance < 0 {
		ep.Importance = 0
	}
	if ep.Importance > 1 {
		ep.Importance = 1
	}
	tagsJSON, _ := json.Marshal(ep.Tags)
	if ep.Tags == nil {
		tagsJSON = []byte("[]")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`INSERT INTO episodes(id, content, context, importance, tags_json, timestamp, session_id)
		 VALUES(?,?,?,?,?,?,?)`,
		ep.ID, ep.Content, ep.Context, ep.Importance, string(tagsJSON),
		ep.Timestamp.UnixMilli(), ep.SessionID); err != nil {
		return fmt.Errorf("agents: storing episode: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO episodes_fts(content, context, tags, episode_id) VALUES(?,?,?,?)`,
		ep.Content, ep.Context, strings.Join(ep.Tags, " "), ep.ID); err != nil {
		return fmt.Errorf("agents: indexing episode: %w", err)
	}
	return tx.Commit()
}

// Recall full-text searches episodes for query and returns up to limit
// results, ranked by importance damped by age on a 30-day scale
// (importance / (1 + age/30d)): recency breaks ties between equally important
// episodes, but an important week-old fact still outranks a trivial one from
// today.
func (s *EpisodicStore) Recall(query string, limit int) ([]Episode, error) {
	return s.recall(query, "", limit)
}

// recall implements Recall with an optional exact-context filter (used by
// ProjectMemory to scope recall to one project).
func (s *EpisodicStore) recall(query, contextFilter string, limit int) ([]Episode, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	// OR the individual terms so a long message still matches an episode
	// indexed under a few of its words.
	terms := strings.Fields(q)
	for i, t := range terms {
		terms[i] = ftsQuote(t)
	}
	where := `WHERE episodes_fts MATCH ?`
	args := []any{strings.Join(terms, " OR ")}
	if contextFilter != "" {
		where += ` AND e.context = ?`
		args = append(args, contextFilter)
	}
	args = append(args, time.Now().UnixMilli(), limit)

	rows, err := s.db.Query(
		`SELECT e.id, e.content, e.context, e.importance, e.tags_json, e.timestamp, e.session_id
		 FROM episodes_fts f JOIN episodes e ON e.id = f.episode_id `+where+`
		 ORDER BY e.importance / (1.0 + (? - e.timestamp) / 2592000000.0) DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("agents: recalling episodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Episode
	for rows.Next() {
		var ep Episode
		var tags string
		var ts int64
		if err := rows.Scan(&ep.ID, &ep.Content, &ep.Context, &ep.Importance,
			&tags, &ts, &ep.SessionID); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tags), &ep.Tags)
		ep.Timestamp = time.UnixMilli(ts)
		out = append(out, ep)
	}
	return out, rows.Err()
}

// storeImportantSystemPrompt asks the model to pick out durable facts from one
// conversation exchange. Trivial exchanges must yield an empty array.
const storeImportantSystemPrompt = `Identify important facts worth remembering across sessions (user preferences, project facts, decisions, deployments). Ignore small talk and transient details. Return only JSON, no prose — an array, empty if nothing is worth remembering:
[{"content": "the fact", "context": "project/task context", "importance": 0.0-1.0, "tags": ["tag"]}]`

// StoreImportant asks the AI whether exchange contains facts worth remembering
// and stores any it identifies. A trivial exchange (the model returns []) is
// not an error. The session is recorded on each stored episode.
func (s *EpisodicStore) StoreImportant(ctx context.Context, gateway AIGateway, session string, exchange string) error {
	if strings.TrimSpace(exchange) == "" {
		return nil
	}
	reply, err := gateway.Complete(ctx, exchange, storeImportantSystemPrompt)
	if err != nil {
		return fmt.Errorf("agents: identifying important facts: %w", err)
	}
	var facts []struct {
		Content    string   `json:"content"`
		Context    string   `json:"context"`
		Importance float64  `json:"importance"`
		Tags       []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(stripCodeFences(reply)), &facts); err != nil {
		return fmt.Errorf("agents: parsing facts JSON: %w", err)
	}
	for _, f := range facts {
		if strings.TrimSpace(f.Content) == "" {
			continue
		}
		if err := s.Store(Episode{
			Content: f.Content, Context: f.Context,
			Importance: f.Importance, Tags: f.Tags, SessionID: session,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ProjectMemory is an EpisodicStore view scoped to one project: stores tag
// episodes with the project path as context, and recalls only consider that
// project's episodes.
type ProjectMemory struct {
	store   *EpisodicStore
	project string
}

// ForProject returns a view of the store scoped to projectPath.
func (s *EpisodicStore) ForProject(projectPath string) *ProjectMemory {
	return &ProjectMemory{store: s, project: projectPath}
}

// Store saves an episode under this project's context.
func (p *ProjectMemory) Store(ep Episode) error {
	ep.Context = p.project
	return p.store.Store(ep)
}

// Recall searches only this project's episodes.
func (p *ProjectMemory) Recall(query string, limit int) ([]Episode, error) {
	return p.store.recall(query, p.project, limit)
}
