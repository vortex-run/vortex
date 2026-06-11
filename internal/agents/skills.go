package agents

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Skill is a reusable procedure learned from a completed task (build plan
// upgrade 1 — self-improving agent). Once saved, the coordinator surfaces
// matching skills in the system prompt so the agent follows a proven procedure
// instead of reasoning from scratch.
type Skill struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`         // short human name
	Description string      `json:"description"`  // what this skill does
	Trigger     []string    `json:"trigger"`      // keywords that activate it
	Steps       []SkillStep `json:"steps"`        // the procedure
	CreatedFrom string      `json:"created_from"` // task ID that created it
	UsedCount   int         `json:"used_count"`
	SuccessRate float64     `json:"success_rate"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// SkillStep is one step of a skill's procedure.
type SkillStep struct {
	Description string         `json:"description"`
	ToolName    string         `json:"tool_name,omitempty"` // which tool to call
	Params      map[string]any `json:"params,omitempty"`
	IsOptional  bool           `json:"optional,omitempty"`
}

// SkillStats summarises the store for the dashboard/CLI.
type SkillStats struct {
	Total          int     `json:"total"`
	AvgSuccessRate float64 `json:"avg_success_rate"`
	MostUsed       string  `json:"most_used"`
}

// SkillStore persists learned skills in SQLite with an FTS5 index over name,
// description and trigger keywords so Find can match free-form task text. It
// is safe for concurrent use (single connection, WAL mode), mirroring
// MemoryStore.
type SkillStore struct {
	db   *sql.DB
	path string
}

// skillsSchema is applied on open; IF NOT EXISTS makes it idempotent. The FTS
// table is maintained manually inside Save/Delete transactions (rather than by
// triggers) because trigger keywords are stored as JSON in the main table but
// indexed as plain space-joined text.
const skillsSchema = `
CREATE TABLE IF NOT EXISTS skills (
  id               TEXT PRIMARY KEY,
  name             TEXT NOT NULL,
  description      TEXT NOT NULL,
  trigger_keywords TEXT NOT NULL DEFAULT '[]',
  steps_json       TEXT NOT NULL DEFAULT '[]',
  created_from     TEXT NOT NULL DEFAULT '',
  used_count       INTEGER NOT NULL DEFAULT 0,
  success_rate     REAL NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS skills_fts USING fts5(
  name, description, trigger_keywords, skill_id UNINDEXED
);
`

// NewSkillStore opens (creating if needed) a SQLite-backed skill store at
// dbPath, applying the schema and enabling WAL + busy timeout.
func NewSkillStore(dbPath string) (*SkillStore, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("agents: creating skills dir: %w", err)
		}
	}
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("agents: opening skills db: %w", err)
	}
	// SQLite is a single file; one writer at a time avoids "database is locked".
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(skillsSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agents: applying skills schema: %w", err)
	}
	return &SkillStore{db: db, path: dbPath}, nil
}

// Close closes the underlying database.
func (s *SkillStore) Close() error { return s.db.Close() }

// Save inserts or replaces a skill and refreshes its FTS index entry. A skill
// without an ID gets a generated one; CreatedAt/UpdatedAt are filled in when
// zero.
func (s *SkillStore) Save(skill *Skill) error {
	if skill == nil {
		return fmt.Errorf("agents: nil skill")
	}
	if strings.TrimSpace(skill.Name) == "" {
		return fmt.Errorf("agents: skill name required")
	}
	if skill.ID == "" {
		id, err := randomSessionID()
		if err != nil {
			return err
		}
		skill.ID = id
	}
	now := time.Now()
	if skill.CreatedAt.IsZero() {
		skill.CreatedAt = now
	}
	skill.UpdatedAt = now

	triggersJSON, _ := json.Marshal(skill.Trigger)
	if skill.Trigger == nil {
		triggersJSON = []byte("[]")
	}
	stepsJSON, _ := json.Marshal(skill.Steps)
	if skill.Steps == nil {
		stepsJSON = []byte("[]")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`INSERT INTO skills(id, name, description, trigger_keywords, steps_json,
		   created_from, used_count, success_rate, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name, description=excluded.description,
		   trigger_keywords=excluded.trigger_keywords, steps_json=excluded.steps_json,
		   created_from=excluded.created_from, used_count=excluded.used_count,
		   success_rate=excluded.success_rate, updated_at=excluded.updated_at`,
		skill.ID, skill.Name, skill.Description, string(triggersJSON), string(stepsJSON),
		skill.CreatedFrom, skill.UsedCount, skill.SuccessRate,
		skill.CreatedAt.UnixMilli(), skill.UpdatedAt.UnixMilli()); err != nil {
		return fmt.Errorf("agents: saving skill: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM skills_fts WHERE skill_id=?`, skill.ID); err != nil {
		return fmt.Errorf("agents: refreshing skill index: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO skills_fts(name, description, trigger_keywords, skill_id) VALUES(?,?,?,?)`,
		skill.Name, skill.Description, strings.Join(skill.Trigger, " "), skill.ID); err != nil {
		return fmt.Errorf("agents: indexing skill: %w", err)
	}
	return tx.Commit()
}

// Find full-text searches name, description and trigger keywords for skills
// relevant to query, ranked by proven usefulness (success_rate * used_count,
// then recency). An empty query returns nothing.
func (s *SkillStore) Find(query string) ([]*Skill, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	// OR the individual terms so a long task description still matches a skill
	// indexed under a few of its keywords.
	terms := strings.Fields(q)
	for i, t := range terms {
		terms[i] = ftsQuote(t)
	}
	rows, err := s.db.Query(
		`SELECT s.id, s.name, s.description, s.trigger_keywords, s.steps_json,
		        s.created_from, s.used_count, s.success_rate, s.created_at, s.updated_at
		 FROM skills_fts f JOIN skills s ON s.id = f.skill_id
		 WHERE skills_fts MATCH ?
		 ORDER BY s.success_rate * s.used_count DESC, s.updated_at DESC
		 LIMIT 20`,
		strings.Join(terms, " OR "))
	if err != nil {
		return nil, fmt.Errorf("agents: searching skills: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanSkills(rows)
}

// MarkUsed records one use of a skill, updating used_count and the running
// success rate.
func (s *SkillStore) MarkUsed(id string, success bool) error {
	hit := 0.0
	if success {
		hit = 1.0
	}
	res, err := s.db.Exec(
		`UPDATE skills SET
		   success_rate = (success_rate * used_count + ?) / (used_count + 1),
		   used_count   = used_count + 1,
		   updated_at   = ?
		 WHERE id=?`,
		hit, time.Now().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("agents: marking skill used: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("agents: skill %s not found", id)
	}
	return nil
}

// List returns all skills, most recently updated first.
func (s *SkillStore) List() ([]*Skill, error) {
	rows, err := s.db.Query(
		`SELECT id, name, description, trigger_keywords, steps_json,
		        created_from, used_count, success_rate, created_at, updated_at
		 FROM skills ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("agents: listing skills: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanSkills(rows)
}

// Delete removes a skill and its FTS entry.
func (s *SkillStore) Delete(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM skills WHERE id=?`, id); err != nil {
		return fmt.Errorf("agents: deleting skill: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM skills_fts WHERE skill_id=?`, id); err != nil {
		return fmt.Errorf("agents: deleting skill index: %w", err)
	}
	return tx.Commit()
}

// Stats returns aggregate counts for the dashboard/CLI.
func (s *SkillStore) Stats() SkillStats {
	var st SkillStats
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(AVG(success_rate), 0) FROM skills`).
		Scan(&st.Total, &st.AvgSuccessRate)
	_ = s.db.QueryRow(`SELECT name FROM skills ORDER BY used_count DESC LIMIT 1`).
		Scan(&st.MostUsed)
	return st
}

// scanSkills reads skill rows produced by the SELECT column order used in
// Find/List.
func scanSkills(rows *sql.Rows) ([]*Skill, error) {
	var out []*Skill
	for rows.Next() {
		var sk Skill
		var triggers, steps string
		var created, updated int64
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &triggers, &steps,
			&sk.CreatedFrom, &sk.UsedCount, &sk.SuccessRate, &created, &updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(triggers), &sk.Trigger)
		_ = json.Unmarshal([]byte(steps), &sk.Steps)
		sk.CreatedAt = time.UnixMilli(created)
		sk.UpdatedAt = time.UnixMilli(updated)
		out = append(out, &sk)
	}
	return out, rows.Err()
}
