// This file implements RunStore: SQLite-backed durability for orchestration
// runs (production audit H3). The planned task DAG, every task state
// transition, and the shared memory are persisted so an interrupted run can be
// resumed after a crash at task granularity — completed tasks keep their
// results and are never re-executed (exactly-once); tasks that were mid-flight
// re-run (at-least-once). It follows the SQLite conventions established by
// internal/agents/workflow.go (same driver, DSN pragmas, single connection,
// idempotent schema).
package orchestration

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Run statuses. A run is resumable while "running"; "done" and "failed" are
// terminal.
const (
	RunRunning = "running"
	RunDone    = "done"
	RunFailed  = "failed"
)

// RunInfo summarises a stored run for listings and resume.
type RunInfo struct {
	ID        string
	Goal      string
	SessionID string
	Status    string // running, done, or failed
}

// RunStore persists orchestration runs in SQLite. Safe for concurrent use —
// the database/sql pool serialises writes (one connection) and SQLite runs in
// WAL mode for concurrent reads.
type RunStore struct {
	db   *sql.DB
	path string
}

// runSchema is applied on open; CREATE ... IF NOT EXISTS makes it idempotent.
// orch_tasks.idx preserves plan order — Claim fairness depends on queue
// insertion order, so LoadRun must return tasks in the order they were planned.
const runSchema = `
CREATE TABLE IF NOT EXISTS orch_runs (
  id           TEXT PRIMARY KEY,
  goal         TEXT NOT NULL,
  session_id   TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL,
  result       TEXT NOT NULL DEFAULT '',
  resume_count INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_orch_runs_status ON orch_runs(status);

CREATE TABLE IF NOT EXISTS orch_tasks (
  run_id      TEXT NOT NULL,
  id          TEXT NOT NULL,
  idx         INTEGER NOT NULL,
  name        TEXT NOT NULL DEFAULT '',
  agent_type  TEXT NOT NULL DEFAULT '',
  input       TEXT NOT NULL DEFAULT '',
  depends_on  TEXT NOT NULL DEFAULT '[]',
  state       TEXT NOT NULL,
  result      TEXT NOT NULL DEFAULT '',
  error       TEXT NOT NULL DEFAULT '',
  meta        TEXT NOT NULL DEFAULT '{}',
  attempts    INTEGER NOT NULL DEFAULT 0,
  started_at  INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, id),
  FOREIGN KEY (run_id) REFERENCES orch_runs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS orch_memory (
  run_id     TEXT NOT NULL,
  key        TEXT NOT NULL,
  value      TEXT NOT NULL DEFAULT 'null',
  author     TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (run_id, key),
  FOREIGN KEY (run_id) REFERENCES orch_runs(id) ON DELETE CASCADE
);
`

// NewRunStore opens (creating if needed) a SQLite-backed run store at dbPath,
// applying the schema and enabling WAL + foreign keys.
func NewRunStore(dbPath string) (*RunStore, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("orchestration: creating run store dir: %w", err)
		}
	}
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("orchestration: opening run db: %w", err)
	}
	// SQLite is a single file; one writer at a time avoids "database is locked".
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(runSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("orchestration: applying run schema: %w", err)
	}
	return &RunStore{db: db, path: dbPath}, nil
}

// Close closes the underlying database.
func (s *RunStore) Close() error { return s.db.Close() }

// newRunID returns a 32-hex-char run identifier (16 random bytes) — the same
// convention as the agents package's session/workflow IDs.
func newRunID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("orchestration: generating run id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// CreateRun persists a new running run with its full planned DAG in one
// transaction and returns the run ID.
func (s *RunStore) CreateRun(goal, sessionID string, tasks []*Task) (string, error) {
	id, err := newRunID()
	if err != nil {
		return "", err
	}
	now := time.Now().UnixMilli()

	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`INSERT INTO orch_runs(id, goal, session_id, status, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		id, goal, sessionID, RunRunning, now, now); err != nil {
		return "", fmt.Errorf("orchestration: inserting run: %w", err)
	}
	for i, t := range tasks {
		if err := upsertTask(tx, id, i, t); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// SaveTask upserts one task's current state. When t.Attempts is set (>0) it is
// written as the authoritative attempt count (queue Claim increments it; see
// upsertTask). When Attempts is zero and the task is running, the legacy SQL
// bump is retained so older callers/tests that only flip State still
// accumulate attempts.
func (s *RunStore) SaveTask(runID string, t *Task) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// idx -1 sentinel: the ON CONFLICT update below never touches idx, and
	// SaveTask is only ever called for tasks CreateRun already inserted.
	if err := upsertTask(tx, runID, -1, t); err != nil {
		return err
	}
	if t.Attempts == 0 && t.State == StateRunning {
		if _, err := tx.Exec(
			`UPDATE orch_tasks SET attempts = attempts + 1 WHERE run_id=? AND id=?`,
			runID, t.ID); err != nil {
			return fmt.Errorf("orchestration: bumping task attempts: %w", err)
		}
	}
	return tx.Commit()
}

// SyncTasks upserts every task's state in one transaction — the end-of-run
// reconcile that captures transitions made inside the queue (dep-failure marks
// in Claim/drainBlocked never pass through runTask) and self-corrects any
// dropped best-effort writes.
func (s *RunStore) SyncTasks(runID string, tasks []*Task) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, t := range tasks {
		if err := upsertTask(tx, runID, -1, t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// upsertTask inserts or updates one task row. idx is only written on insert
// (CreateRun); updates never move a task's plan position. An empty state is
// stored as pending — planner tasks arrive unset and the queue's Add applies
// the same default. attempts is written when t.Attempts > 0 (the queue's
// authoritative count) and left untouched when zero, so legacy state-only
// writers never regress it and SyncTasks reconciles it like every other field.
func upsertTask(tx *sql.Tx, runID string, idx int, t *Task) error {
	state := t.State
	if state == "" {
		state = StatePending
	}
	deps, err := json.Marshal(t.DependsOn)
	if err != nil {
		return fmt.Errorf("orchestration: marshaling task deps: %w", err)
	}
	if t.DependsOn == nil {
		deps = []byte("[]")
	}
	metaMap, err := taskMetaForUpsert(tx, runID, idx, t)
	if err != nil {
		return err
	}
	meta, err := json.Marshal(metaMap)
	if err != nil {
		return fmt.Errorf("orchestration: marshaling task meta: %w", err)
	}
	_, err = tx.Exec(
		`INSERT INTO orch_tasks(run_id, id, idx, name, agent_type, input, depends_on, state, result, error, meta, attempts, started_at, finished_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(run_id, id) DO UPDATE SET
		   state=excluded.state, result=excluded.result, error=excluded.error,
		   meta=excluded.meta,
		   attempts=CASE WHEN excluded.attempts > 0 THEN excluded.attempts ELSE orch_tasks.attempts END,
		   started_at=excluded.started_at, finished_at=excluded.finished_at`,
		runID, t.ID, idx, t.Name, t.AgentType, t.Input, string(deps),
		string(state), t.Result, t.Error, string(meta), t.Attempts,
		unixMilliOrZero(t.StartedAt), unixMilliOrZero(t.FinishedAt))
	if err != nil {
		return fmt.Errorf("orchestration: upserting task %s: %w", t.ID, err)
	}
	return nil
}

// taskMetaForUpsert builds the JSON meta blob for a task row. CreateRun passes
// idx >= 0; SaveTask/SyncTasks use idx -1 for updates.
//
// Contract: a non-empty t.Meta replaces the stored meta wholesale (no merge).
// An empty or nil t.Meta on update preserves the existing DB meta so
// state-only SaveTask calls (resume simulators, concurrent state writes) do
// not wipe planner fields such as required_capabilities. Consequently meta
// cannot be cleared to {} through SaveTask/SyncTasks; the queue only ever
// merges keys (SetMeta), so no caller needs to. MaxAttempts and
// VerificationCriteria are struct fields and are re-stamped into meta from the
// task on every write (not read back from the row).
//
// Stored meta that is not a JSON object (corruption or hand edits) is dropped
// and rewritten from the task: it is unreadable to LoadRun anyway, and failing
// the write would stall the run for the sake of bytes we cannot interpret.
func taskMetaForUpsert(tx *sql.Tx, runID string, idx int, t *Task) (map[string]any, error) {
	metaMap := map[string]any{}
	if len(t.Meta) > 0 {
		for k, v := range t.Meta {
			metaMap[k] = v
		}
	} else if idx < 0 {
		var existing sql.NullString
		err := tx.QueryRow(
			`SELECT meta FROM orch_tasks WHERE run_id=? AND id=?`, runID, t.ID).
			Scan(&existing)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("orchestration: reading task meta: %w", err)
		}
		if existing.Valid && existing.String != "" && existing.String != "{}" {
			// json.Unmarshal of a JSON `null` sets the map to nil (and leaves
			// it nil on error), so always re-establish a writable map.
			if uerr := json.Unmarshal([]byte(existing.String), &metaMap); uerr != nil || metaMap == nil {
				metaMap = map[string]any{}
			}
		}
	}
	if t.MaxAttempts > 0 {
		metaMap["max_attempts"] = t.MaxAttempts
	}
	if t.VerificationCriteria != "" {
		metaMap["verification_criteria"] = t.VerificationCriteria
	}
	return metaMap, nil
}

// SyncMemory upserts a shared-memory snapshot for the run in one transaction.
// Values are JSON-encoded (executor results are strings in practice).
func (s *RunStore) SyncMemory(runID string, snap map[string]MemoryEntry) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for key, e := range snap {
		val, merr := json.Marshal(e.Value)
		if merr != nil {
			// Unserializable values (shouldn't occur — results are strings)
			// are stored as null rather than failing the whole snapshot.
			val = []byte("null")
		}
		if _, err := tx.Exec(
			`INSERT INTO orch_memory(run_id, key, value, author, updated_at) VALUES(?,?,?,?,?)
			 ON CONFLICT(run_id, key) DO UPDATE SET
			   value=excluded.value, author=excluded.author, updated_at=excluded.updated_at`,
			runID, key, string(val), e.Author, e.UpdatedAt.UnixMilli()); err != nil {
			return fmt.Errorf("orchestration: upserting memory %s: %w", key, err)
		}
	}
	return tx.Commit()
}

// FinishRun marks a run terminal with its final status (done/failed) and a
// result summary or failure reason.
func (s *RunStore) FinishRun(runID, status, result string) error {
	_, err := s.db.Exec(
		`UPDATE orch_runs SET status=?, result=?, updated_at=? WHERE id=?`,
		status, result, time.Now().UnixMilli(), runID)
	return err
}

// MarkResumed increments and returns the run's resume counter. The caller caps
// it (maxResumeAttempts) so a poison run that crashes the process cannot loop
// through resume forever.
func (s *RunStore) MarkResumed(runID string) (int, error) {
	if _, err := s.db.Exec(
		`UPDATE orch_runs SET resume_count = resume_count + 1, updated_at=? WHERE id=?`,
		time.Now().UnixMilli(), runID); err != nil {
		return 0, err
	}
	var n int
	err := s.db.QueryRow(`SELECT resume_count FROM orch_runs WHERE id=?`, runID).Scan(&n)
	return n, err
}

// ListIncomplete returns resumable runs (status running), oldest first.
func (s *RunStore) ListIncomplete() ([]RunInfo, error) {
	rows, err := s.db.Query(
		`SELECT id, goal, session_id, status FROM orch_runs WHERE status=? ORDER BY created_at ASC, rowid ASC`,
		RunRunning)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RunInfo
	for rows.Next() {
		var r RunInfo
		if err := rows.Scan(&r.ID, &r.Goal, &r.SessionID, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRecent returns runs ordered by updated_at descending (newest first).
// Terminal and in-progress runs are included. limit <= 0 returns all runs.
// updated_at has millisecond resolution, so rowid (insertion order) breaks
// ties; without it same-millisecond runs come back in an unspecified order
// and a LIMIT can drop a different run on each call.
func (s *RunStore) ListRecent(limit int) ([]RunInfo, error) {
	q := `SELECT id, goal, session_id, status FROM orch_runs ORDER BY updated_at DESC, rowid DESC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RunInfo
	for rows.Next() {
		var r RunInfo
		if err := rows.Scan(&r.ID, &r.Goal, &r.SessionID, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadRun returns a run's tasks (in plan order) and shared-memory entries.
func (s *RunStore) LoadRun(runID string) ([]*Task, []MemoryEntry, error) {
	rows, err := s.db.Query(
		`SELECT id, name, agent_type, input, depends_on, state, result, error, meta, attempts, started_at, finished_at
		 FROM orch_tasks WHERE run_id=? ORDER BY idx ASC`, runID)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()

	var tasks []*Task
	for rows.Next() {
		var t Task
		var deps, meta, state string
		var started, finished int64
		if err := rows.Scan(&t.ID, &t.Name, &t.AgentType, &t.Input, &deps,
			&state, &t.Result, &t.Error, &meta, &t.Attempts, &started, &finished); err != nil {
			return nil, nil, err
		}
		t.State = TaskState(state)
		_ = json.Unmarshal([]byte(deps), &t.DependsOn)
		// Best-effort: these columns are only ever written by json.Marshal
		// here, so a decode failure means external corruption. The task still
		// loads (with nil deps/meta and default MaxAttempts) rather than
		// making the whole run unloadable/unresumable.
		_ = json.Unmarshal([]byte(meta), &t.Meta)
		normalizeTaskMetaFromJSON(t.Meta)
		if t.Meta != nil {
			if v, ok := t.Meta["max_attempts"].(float64); ok && int(v) > 0 {
				t.MaxAttempts = int(v)
				delete(t.Meta, "max_attempts")
			}
			if v, ok := t.Meta["verification_criteria"].(string); ok {
				t.VerificationCriteria = v
				delete(t.Meta, "verification_criteria")
			}
			if len(t.Meta) == 0 {
				t.Meta = nil
			}
		}
		if started > 0 {
			t.StartedAt = time.UnixMilli(started)
		}
		if finished > 0 {
			t.FinishedAt = time.UnixMilli(finished)
		}
		tasks = append(tasks, &t)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	mrows, err := s.db.Query(
		`SELECT key, value, author, updated_at FROM orch_memory WHERE run_id=?`, runID)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = mrows.Close() }()
	var memory []MemoryEntry
	for mrows.Next() {
		var e MemoryEntry
		var val string
		var updated int64
		if err := mrows.Scan(&e.Key, &val, &e.Author, &updated); err != nil {
			return nil, nil, err
		}
		_ = json.Unmarshal([]byte(val), &e.Value)
		e.UpdatedAt = time.UnixMilli(updated)
		memory = append(memory, e)
	}
	return tasks, memory, mrows.Err()
}

// normalizeTaskMetaFromJSON repairs Meta values after json.Unmarshal into
// map[string]any (JSON arrays become []any, not []string).
func normalizeTaskMetaFromJSON(meta map[string]any) {
	if meta == nil {
		return
	}
	if v, ok := meta[metaRequiredCapabilities]; ok {
		if caps, err := coerceStringSlice(v); err == nil {
			meta[metaRequiredCapabilities] = caps
		}
	}
}

func coerceStringSlice(v any) ([]string, error) {
	switch s := v.(type) {
	case []string:
		return s, nil
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("orchestration: meta slice element is not a string")
			}
			out = append(out, str)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("orchestration: meta value is not a string slice")
	}
}

// unixMilliOrZero converts a time to unix ms, mapping the zero time to 0 so
// unset timestamps round-trip as zero values.
func unixMilliOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
